package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	_ "modernc.org/sqlite"

	"ntikuma/internal/config"
	"ntikuma/internal/platform"
	"ntikuma/internal/secrets"
	"ntikuma/internal/services"
	"ntikuma/internal/store"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/tray.png
var trayIcon []byte

const appID = "dev.divinetettey.ntikuma"

var version = "0.1.0-dev"

// main function serves as the application's entry point. It initializes the application, creates a window,
// and starts a goroutine that emits a time-based event every second. It subsequently runs the application and
// logs any error that might occur.
func main() {
	cmd, err := parseCommand(os.Args[1:])

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		os.Exit(2)
	}

	switch cmd {
	case cmdHelp:
		usage()
		return
	case cmdVersion:
		fmt.Println("ntikuma", version)
		return
	}

	popup := &Popup{}

	app := application.New(application.Options{
		Name:        "Ntikuma",
		Description: "Ask an LLM from anywhere on your desktop",
		LogLevel:    slog.LevelInfo,
		Services: []application.Service{
			application.NewService(&GreetService{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: appID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				c, err := parseCommand(argsAfterProgram(data.Args))
				if err != nil {
					log.Printf("ignoring second instance: %v", err)
					return
				}
				if c == cmdStart {
					c = cmdShow // launching from the app grid should show the popup
				}
				popup.Handle(c, data.AdditionalData["activationToken"]) // activationToken is set by the platform-specific code
			},
			AdditionalData: map[string]string{
				"activationToken": platform.ActivationToken(),
			},
		},
		Linux: application.LinuxOptions{
			ApplicationID:                 appID,
			DisableQuitOnLastWindowClosed: true,
		},
	})

	if cmd == cmdQuit {
		return
	}

	paths := config.DefaultPaths()
	if err := paths.Ensure(); err != nil {
		log.Fatal(err)
	}

	cfg, err := config.Load(paths.ConfigFile)
	if err != nil {
		log.Fatal(err)
	}
	db, err := store.Open(paths.Database)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	keys := secrets.Keyring{}
	app.RegisterService(application.NewService(services.NewChatService(db, cfg, keys, app.Event)))
	app.RegisterService(application.NewService(services.NewHistoryService(db)))
	app.RegisterService(application.NewService(services.NewSettingsService(cfg, keys, popup.ChooseFolder)))
	app.RegisterService(application.NewService(services.NewVoiceService(cfg, filepath.Join(paths.CacheDir, "voice"), app.Event, popup.ChooseModelFolder)))
	contextService := services.NewContextService(cfg, app.Clipboard, popup.CaptureScreenshot, platform.NewSelectionReader(application.InvokeSync))
	app.RegisterService(application.NewService(contextService))
	popup.prepareSelection = func() *services.SelectionCapture {
		if !cfg.Get().AttachSelection || !contextService.SelectionStatus().BeforeFocus {
			return nil
		}
		result, err := contextService.Selection(context.Background())
		if err != nil {
			result.Message = err.Error()
		}
		return &result
	}
	app.RegisterService(application.NewService(&PopupService{popup: popup}))

	// Create a new window with the necessary options.
	// 'Title' is the title of the window.
	// 'Mac' options tailor the window when running on macOS.
	// 'BackgroundColour' is the background colour of the window.
	// 'URL' is the URL that will be loaded into the webview.
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "popup",
		Title:           "Ntikuma",
		Width:           popupWidth,
		Height:          popupStartH,
		Frameless:       true,
		Hidden:          true,
		AlwaysOnTop:     true,
		InitialPosition: application.WindowCentered,
		BackgroundType:  application.BackgroundTypeTransparent,
		// Popup owns focus-loss dismissal so every hide notifies the frontend.
		HideOnFocusLost: false,
		DevToolsEnabled: os.Getenv("NTIKUMA_DEVTOOLS") != "",
		URL:             "/",
		Linux: application.LinuxWindow{
			WindowIsTranslucent: true,
			WebviewGpuPolicy:    application.WebviewGpuPolicyOnDemand,
		},
	})

	popup.attach(app, win)

	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		popup.Hide()
		e.Cancel()
	})
	if os.Getenv("NTIKUMA_KEEP_OPEN") == "" && !app.Env.HasFocusFollowsMouse() {
		win.OnWindowEvent(events.Common.WindowLostFocus, func(*application.WindowEvent) { popup.LostFocus() })
	}
	win.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) {
		app.Event.Emit(EventFocus)
	})

	setupTray(app, popup)

	if cfg.Get().UsePortalShortcuts {
		registerPortalShortcuts(app, popup)
	}

	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		if days := cfg.Get().KeepChatsDays; days > 0 {
			go func() {
				if n, err := db.Prune(context.Background(), time.Duration(days)*24*time.Hour); err == nil && n > 0 {
					log.Printf("pruned %d old chats", n)
				}
			}()
		}
		if cmd != cmdStart {
			popup.Handle(cmd, platform.ActivationToken())
		}
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// setupTray puts Ntikuma in the top bar. Ubuntu ships the AppIndicator extension, whis shows StatusNotification Icons
func setupTray(app *application.App, popup *Popup) {
	menu := application.NewMenu()
	menu.Add("Open Ntikuma").OnClick(func(*application.Context) { popup.Show("bar", "") })
	menu.Add("Dictate").OnClick(func(*application.Context) { popup.Show("voice", "") })
	menu.Add("History").OnClick(func(*application.Context) { popup.Show("history", "") })
	menu.Add("Preferences").OnClick(func(*application.Context) { popup.Show("settings", "") })
	menu.AddSeparator()
	menu.Add("Quit Ntikuma").OnClick(func(*application.Context) { app.Quit() })

	tray := app.SystemTray.New()
	tray.SetIcon(trayIcon)
	tray.SetTooltip("Ntikuma")
	tray.SetMenu(menu)
	tray.OnClick(func() { popup.Handle(cmdToggle, "") })
}

func registerPortalShortcuts(app *application.App, popup *Popup) {
	bind := func(accel string, c command) {
		if err := app.GlobalShortcut.Register(accel, func() { popup.Handle(c, "") }); err != nil {
			log.Printf("global shortcut %s: %v", accel, err)
		}
	}
	bind("Super+Space", cmdToggle)
	bind("Super+Shift+Space", cmdVoice)
}
