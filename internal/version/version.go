// Package version 是 GUI 侧栏经 App.Version() 绑定取用、CLI 横幅直接引用的
// 版本号来源。
// 改版本需同步本文件 + wails.json + build/windows/winres.json +
// build/windows/wails.exe.manifest + build/installer/installer.iss，并按
// build.bat 头部说明重生成 syso。
package version

const Version = "v1.2.5"

func Display() string { return Version }
