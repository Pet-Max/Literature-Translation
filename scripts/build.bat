@echo off
rem Literature Translation one-click build - produces build\bin\Literature-Translation.exe with embedded icon and version info.
rem Both come from rsrc_windows_amd64.syso in the project root, which already carries
rem the icon, the manifest and the version resources matching internal\version.
rem IMPORTANT: when bumping the version, regenerate the syso with a tool that supports
rem version resources (e.g. goversioninfo with a versioninfo.json template) and commit it
rem before tagging - the release workflow fails if the exe version does not match the tag.
rem (Plain "rsrc -ico ..." regenerates the icon only and would silently drop version info.)
cd /d "%~dp0.."
go build -tags desktop,production -ldflags "-H windowsgui" -o "build/bin/Literature-Translation.exe" .
if %errorlevel%==0 (echo BUILD OK: build\bin\Literature-Translation.exe) else (echo BUILD FAILED)
