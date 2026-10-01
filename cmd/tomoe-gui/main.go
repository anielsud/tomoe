package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"github.com/sosuke-ai/tomoe-pc/internal/backend"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Re-exec with LD_LIBRARY_PATH if GPU libraries are installed.
	// Must happen before any cgo/sherpa-onnx code loads.
	config.EnsureGPULibs()
	setupLogFile()

	app := backend.NewApp()

	err := wails.Run(&options.App{
		Title:     "Tomoe — Meeting Transcription",
		Width:     900,
		Height:    700,
		MinWidth:  640,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:     app.Startup,
		OnShutdown:    app.Shutdown,
		OnBeforeClose: app.BeforeClose,
		Bind: []interface{}{
			app,
		},
		Linux: &linux.Options{
			ProgramName:      "Tomoe",
			WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
