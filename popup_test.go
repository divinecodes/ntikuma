package main

import (
	"encoding/json"
	"testing"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"

	"ntikuma/internal/services"
)

type lifecycleWindow struct {
	visible                bool
	hides                  int
	onHide                 func()
	size, minimum, maximum [2]int
}

func (w *lifecycleWindow) Show() application.Window { w.visible = true; return nil }
func (w *lifecycleWindow) Hide() application.Window {
	w.visible = false
	w.hides++
	if w.onHide != nil {
		w.onHide()
	}
	return nil
}
func (w *lifecycleWindow) Focus()                       {}
func (w *lifecycleWindow) IsVisible() bool              { return w.visible }
func (w *lifecycleWindow) NativeWindow() unsafe.Pointer { return nil }
func (w *lifecycleWindow) SetSize(width, height int) application.Window {
	w.size = [2]int{width, height}
	return nil
}
func (w *lifecycleWindow) SetMinSize(width, height int) application.Window {
	w.minimum = [2]int{width, height}
	return nil
}
func (w *lifecycleWindow) SetMaxSize(width, height int) application.Window {
	w.maximum = [2]int{width, height}
	return nil
}

func TestPopupResizeKeepsNativeBoundsEqualToClampedCardSize(t *testing.T) {
	window := &lifecycleWindow{}
	service := &PopupService{popup: &Popup{win: window}}
	for _, test := range []struct{ requested, want int }{{10, 96}, {537, 537}, {1000, 720}, {145, 145}} {
		service.Resize(test.requested)
		want := [2]int{popupWidth, test.want}
		if window.size != want || window.minimum != want || window.maximum != want {
			t.Fatalf("request %d: size %v, bounds %v / %v", test.requested, window.size, window.minimum, window.maximum)
		}
	}
}

type popupEvents func(string, ...any) bool

func (f popupEvents) Emit(name string, data ...any) bool { return f(name, data...) }

func TestPopupHideNotifiesWithoutSDKHideEventAndIgnoresRepeatedDismissal(t *testing.T) {
	w := &lifecycleWindow{}
	var names []string
	p := &Popup{win: w, events: popupEvents(func(name string, _ ...any) bool { names = append(names, name); return true })}
	p.Show("bar", "")
	// GTK's visibility change can cause another blur callback. It must not
	// duplicate the app's notification or depend on an SDK WindowHide event.
	w.onHide = p.Hide
	p.Hide()
	p.Hide()
	if w.hides != 1 || w.visible {
		t.Fatalf("native hides=%d visible=%v", w.hides, w.visible)
	}
	if len(names) != 2 || names[0] != EventOpen || names[1] != EventHidden {
		t.Fatalf("lifecycle events %v", names)
	}
	p.Show("bar", "")
	p.Hide()
	if len(names) != 4 || names[3] != EventHidden {
		t.Fatalf("second dismissal lost notification: %v", names)
	}
}

func TestOpenEventUsesFrontendModeField(t *testing.T) {
	b, err := json.Marshal(OpenEvent{Mode: "bar"})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["mode"] != "bar" {
		t.Fatalf("frontend cannot read mode from %s", b)
	}
}

func TestFolderDialogPreservesPreferencesAndSuppressesFocusDismissal(t *testing.T) {
	window := &lifecycleWindow{}
	var emitted []string
	popup := &Popup{win: window, events: popupEvents(func(name string, _ ...any) bool { emitted = append(emitted, name); return true })}
	popup.Show("settings", "")
	popup.chooseDirectory = func(initial string) (string, error) {
		if initial != "/initial" {
			t.Fatal(initial)
		}
		popup.LostFocus()
		popup.Show("bar", "")
		if !window.visible || window.hides != 0 || len(emitted) != 1 {
			t.Fatal("dialog dismissed or reset Preferences")
		}
		return "/selected", nil
	}
	if path, err := popup.ChooseModelFolder("/initial"); err != nil || path != "/selected" {
		t.Fatalf("selection: %s %v", path, err)
	}
	popup.LostFocus()
	if window.visible || window.hides != 1 {
		t.Fatal("focus dismissal was not restored")
	}
}

func TestSharedFolderChooserRejectsOverlappingDialogsAndRestoresAfterCancel(t *testing.T) {
	popup := &Popup{}
	popup.chooseDirectory = func(string) (string, error) {
		if _, err := popup.ChooseModelFolder("/other"); err == nil {
			t.Fatal("overlapping voice dialog was allowed")
		}
		return "", nil
	}
	if selected, err := popup.ChooseFolder("/tools"); err != nil || selected != "" {
		t.Fatalf("canceled dialog: %q %v", selected, err)
	}
	if popup.choosingFolder {
		t.Fatal("cancel left folder chooser busy")
	}
}

func TestOpenEventPreservesPreFocusSelectionSnapshot(t *testing.T) {
	b, err := json.Marshal(OpenEvent{Mode: "bar", Selection: &services.SelectionCapture{Attachment: &services.Attachment{Kind: "selection", Label: "Selection", Content: "before focus"}}})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Mode      string `json:"mode"`
		Selection struct {
			Attachment services.Attachment `json:"attachment"`
		} `json:"selection"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Mode != "bar" || payload.Selection.Attachment.Content != "before focus" {
		t.Fatalf("selection snapshot lost: %s", b)
	}
}
