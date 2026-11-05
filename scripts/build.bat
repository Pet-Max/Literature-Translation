@echo off
rem Literature Translation one-click build - produces build\bin\Literature-Translation.exe with embedded icon and version info.
rem Both come from rsrc_windows_amd64.syso in the project root, which is generated from
rem build\windows\winres.json (icon groups #1/#2/#3 from the icon-*.png set, manifest, version info).
rem
rem WHEN BUMPING THE VERSION: update the version strings in build\windows\winres.json AND the
rem manifest version in build\windows\wails.exe.manifest, then regenerate and commit the syso
rem BEFORE tagging - the release workflow fails if the exe version does not match the tag:
rem   go run github.com/tc-hib/go-winres@latest make --in build/windows/winres.json --arch amd64
rem (plain "rsrc -ico ..." regenerates the icon only and would silently drop version info.)
cd /d "%~dp0.."
go build -tags desktop,production -ldflags "-H windowsgui" -o "build/bin/Literature-Translation.exe" .
if %errorlevel%==0 (echo BUILD OK: build\bin\Literature-Translation.exe) else (echo BUILD FAILED)
