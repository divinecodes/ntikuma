package main

import (
	"context"
	"errors"
	"sync"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"

	"ntikuma/internal/platform"
	"ntikuma/internal/screenshot"
	"ntikuma/internal/services"
)

const (
	popupWidth     = 720
	popupMinHeight = 96
	popupMaxHeight = 720
	popupStartH    = 360
)

// Events the frontend listen for
const (
	EventOpen   = "ntikuma:open"
	EventHidden = "ntikuma:hidden"
	EventFocus  = "ntikuma:focus"
)

// OpenEvent tells the frontend which view to show
type OpenEvent struct {
	Mode      string                     `json:"mode"`
	Selection *services.SelectionCapture `json:"selection,omitempty"`
}

// popupWindow is the native window behavior used by the popup lifecycle.
type popupWindow interface {
	Show() application.Window
	Hide() application.Window
	Focus()
	IsVisible() bool
	NativeWindow() unsafe.Pointer
	SetSize(int, int) application.Window
	SetMinSize(int, int) application.Window
	SetMaxSize(int, int) application.Window
}

// popup ownsw the single overlay window
type Popup struct {
	mu               sync.Mutex
	app              *application.App
	win              popupWindow
	events           services.Emitter
	shown            bool
	capturing        bool
	choosingFolder   bool
	chooseDirectory  func(string) (string, error)
	prepareSelection func() *services.SelectionCapture
}

func (p *Popup) attach(app *application.App, win *application.WebviewWindow) {
	p.mu.Lock()
	p.app, p.win = app, win
	p.events = app.Event
	p.chooseDirectory = func(initial string) (string, error) {
		return app.Dialog.OpenFile().SetTitle("Select folder").SetDirectory(initial).
			CanChooseFiles(false).CanChooseDirectories(true).AttachToWindow(win).PromptForSingleSelection()
	}
	p.mu.Unlock()
}

func (p *Popup) window() (app *application.App, win popupWindow) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.app, p.win
}

// handle runs a CLI comand, from argv or from a second instance
func (p *Popup) Handle(c command, activationString string) {
	app, win := p.window()
	if win == nil {
		return
	}

	switch c {
	case cmdToggle:
		if win.IsVisible() {
			p.Hide()
			return
		}
		p.Show("bar", activationString)
	case cmdShow:
		p.Show("bar", activationString)
	case cmdVoice:
		p.Show("voice", activationString)
	case cmdHistory:
		p.Show("history", activationString)
	case cmdSettings:
		p.Show("settings", activationString)
	case cmdQuit:
		app.Quit()
	}
}

// Show raise the popup in the given mode
func (p *Popup) Show(mode, activationToken string) {
	p.mu.Lock()
	capturing := p.capturing || p.choosingFolder
	p.mu.Unlock()
	if capturing {
		return
	}
	_, win := p.window()
	if win == nil {
		return
	}

	var selected *services.SelectionCapture
	if !win.IsVisible() && (mode == "bar" || mode == "voice") && p.prepareSelection != nil {
		selected = p.prepareSelection()
	}
	if activationToken != "" {
		application.InvokeSync(func() {
			platform.SetStartupID(win.NativeWindow(), activationToken)
		})
	}

	p.mu.Lock()
	p.shown = true
	p.mu.Unlock()
	win.Show()
	win.Focus()
	p.events.Emit(EventOpen, OpenEvent{Mode: mode, Selection: selected})
}

// Native dialogs own focus while open. Keep Preferences and its unsaved draft.
func (p *Popup) LostFocus() {
	p.mu.Lock()
	choosing := p.choosingFolder
	p.mu.Unlock()
	if !choosing {
		p.Hide()
	}
}

func (p *Popup) ChooseModelFolder(initial string) (string, error) {
	return p.ChooseFolder(initial)
}

func (p *Popup) ChooseFolder(initial string) (string, error) {
	p.mu.Lock()
	if p.choosingFolder || p.capturing || p.chooseDirectory == nil {
		p.mu.Unlock()
		return "", errors.New("folder dialog is unavailable")
	}
	p.choosingFolder = true
	picker := p.chooseDirectory
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.choosingFolder = false
		shown, win := p.shown, p.win
		p.mu.Unlock()
		if shown && win != nil {
			win.Focus()
		}
	}()
	return picker(initial)
}

// CaptureScreenshot restores the existing view without an open event, which
// would otherwise reset a new-chat draft. Keep shortcuts from raising the
// popup over the portal's capture UI.
func (p *Popup) CaptureScreenshot(ctx context.Context) (screenshot.Image, error) {
	p.mu.Lock()
	win := p.win
	if win == nil || p.capturing {
		p.mu.Unlock()
		return screenshot.Image{}, errors.New("screenshot window is unavailable")
	}
	p.capturing = true
	p.mu.Unlock()
	p.Hide()
	defer func() {
		p.mu.Lock()
		p.capturing = false
		p.mu.Unlock()
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			p.mu.Lock()
			p.shown = true
			p.mu.Unlock()
			win.Show()
			win.Focus()
		}
	}()
	return screenshot.Capture(ctx)
}

// hides the popup. Streams running in the background
func (p *Popup) Hide() {
	p.mu.Lock()
	win, events := p.win, p.events
	if win == nil || !p.shown {
		p.mu.Unlock()
		return
	}
	p.shown = false
	p.mu.Unlock()
	win.Hide()
	// GTK4 does not emit Common.WindowHide. Emit the app lifecycle event here
	// so keyboard, tray, close, screenshot, and blur dismissal all notify JS.
	events.Emit(EventHidden)
}

// PopupService is bound to the frontend
type PopupService struct {
	popup *Popup
}

// hide the popup (Esc)
func (s *PopupService) Hide() { s.popup.Hide() }

func (s *PopupService) Resize(height int) {
	_, win := s.popup.window()
	if win == nil {
		return
	}

	if height < popupMinHeight {
		height = popupMinHeight
	}
	if height > popupMaxHeight {
		height = popupMaxHeight
	}
	// GTK may retain a shown window's allocated size after SetSize changes its
	// default. This popup follows the card, so keep its native bounds equal too.
	win.SetMinSize(popupWidth, height)
	win.SetMaxSize(popupWidth, height)
	win.SetSize(popupWidth, height)
}

func (s *PopupService) Quit() {
	if app, _ := s.popup.window(); app != nil {
		app.Quit()
	}
}
