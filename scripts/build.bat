@echo off
rem Literature Translation one-click build - produces build\bin\Literature-Translation.exe with embedded icon.
rem The icon comes from rsrc_windows_amd64.syso in the project root.
rem To regenerate the syso after changing build\windows\icon.ico, run:
rem   rsrc -arch amd64 -ico "build/windows/icon.ico,build/windows/icon.ico,build/windows/icon.ico" -o rsrc_windows_amd64.syso
rem (the same ico three times occupies icon IDs 1,2,3 - Wails reads ID 3 for the window icon)
cd /d "%~dp0.."
go build -tags desktop,production -ldflags "-H windowsgui" -o "build/bin/Literature-Translation.exe" .
if %errorlevel%==0 (echo BUILD OK: build\bin\Literature-Translation.exe) else (echo BUILD FAILED)
