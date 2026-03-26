package main

import (
	"context"
	"embed"

	_ "EasyDownload/internal/platformfix"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var frontendAssets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:         "EasyDownload",
		Width:         1200,
		Height:        800,
		MinWidth:      900,
		MinHeight:     600,
		DisableResize: false,
		AssetServer: &assetserver.Options{
			Assets: frontendAssets,
		},
		BackgroundColour: &options.RGBA{R: 18, G: 18, B: 18, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose: func(ctx context.Context) (prevent bool) {
			// Allow programmatic quits (tray/menu/confirmed exit) without re-triggering the close dialog
			if app.IsQuitRequested() {
				return false
			}

			// If user chose "don't ask again" with a specific action, perform it directly
			if app.IsDontAskOnClose() && app.GetCloseAction() != "" {
				if app.GetCloseAction() == "minimize" {
					app.applyMinimizeToTray(ctx)
					return true // Prevent close
				}
				// action == "exit", allow close
				return false
			}

			// Otherwise, show the close confirmation dialog in the frontend
			runtime.EventsEmit(ctx, "app:beforeClose")
			return true // Prevent close, let frontend handle it
		},
		Bind: []any{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
