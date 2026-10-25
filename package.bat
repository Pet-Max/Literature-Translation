@echo off
REM package.bat - produces two local artifacts from one payload:
REM   portable  build\bin\Literature-Translation-<ver>-windows-amd64-portable.zip
REM   installer build\bin\Literature-Translation-Setup-<ver>-windows-amd64.exe (needs Inno Setup 6)
REM payload: main exe + engine\*.py + runtime\ (portable python; run build\prepare_runtime.bat first)
setlocal
cd /d "%~dp0"
chcp 65001 >nul

rem version single source: internal/version
for /f "tokens=4" %%v in ('findstr /C:"const Version" internal\version\version.go') do set VER=%%v
set VER=%VER:"=%
set VERNUM=%VER:v=%
echo packaging %VER%

echo [1/4] building main exe ...
call build.bat
if errorlevel 1 exit /b 1

echo [2/4] staging payload ...
set STAGE=build\package\Literature-Translation
if exist "build\package" rmdir /s /q "build\package"
mkdir "%STAGE%\engine"
copy /y "build\bin\Literature-Translation.exe" "%STAGE%\" >nul
for %%f in (engine\*.py) do copy /y "%%f" "%STAGE%\engine\" >nul
if not exist "build\runtime\python.exe" (
    echo [!] runtime missing - run build\prepare_runtime.bat first
    exit /b 1
)
robocopy "build\runtime" "%STAGE%\runtime" /E /NFL /NDL /NJH /NJS >nul
if errorlevel 8 exit /b 1
copy /y LICENSE "%STAGE%\" >nul

echo [3/4] portable zip ...
powershell -NoProfile -Command "Compress-Archive -Force -Path 'build\package\Literature-Translation' -DestinationPath 'build\bin\Literature-Translation-%VER%-windows-amd64-portable.zip'"

echo [4/4] installer (Inno Setup) ...
set ISCC="%ProgramFiles(x86)%\Inno Setup 6\ISCC.exe"
if not exist %ISCC% set ISCC="%ProgramFiles%\Inno Setup 6\ISCC.exe"
if not exist %ISCC% set ISCC="%LocalAppData%\Programs\Inno Setup 6\ISCC.exe"
if not exist %ISCC% (
    echo [!] ISCC not found - winget install JRSoftware.InnoSetup
    exit /b 1
)
%ISCC% /DAppVersion=%VERNUM% build\installer\installer.iss
if errorlevel 1 exit /b 1

echo.
echo DONE:
echo   build\bin\Literature-Translation-%VER%-windows-amd64-portable.zip
echo   build\bin\Literature-Translation-Setup-%VER%-windows-amd64.exe
