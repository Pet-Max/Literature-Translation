package main

import (
	"embed"

	"literature-translation/app"
	"literature-translation/internal/render"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	a := app.NewApp()
	err := wails.Run(&options.App{
		Title:         "Literature Translation",
		Width:         1040,
		Height:        680,
		MinWidth:      880,
		MinHeight:     600,
		Frameless:     true,
		DisableResize: false,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: render.AssetHandler(), // 预览与导出同源（katex/lxgw 字体）
		},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: false,
			CSSDropProperty:    "--wails-drop-target",
		},
		OnStartup:     a.Startup,
		OnBeforeClose: a.BeforeClose,
		OnShutdown:    a.Shutdown,
		Bind:          []interface{}{a},
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
