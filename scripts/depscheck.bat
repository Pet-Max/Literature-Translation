@echo off
rem MUST stay GBK-encoded (zh-CN cmd parses this file in ANSI cp936). NEVER re-save as UTF-8.
rem ============================================================
rem depscheck.bat —— Literature Translation 依赖检测脚本
rem
rem 检测本机是否具备运行主程序的两个系统依赖：
rem   1) WebView2 运行时 —— 主程序界面（GUI）必需，缺失则窗口无法创建
rem   2) 导出浏览器 —— 仅 PDF 导出必需（无头打印）：Edge 优先，Chrome 自动兜底；缺失不影响其他功能
rem
rem 用法：双击运行（结束后等待回车）；或在控制台/脚本中调用（看退出码）。
rem 退出码：0 = 依赖齐全；1 = 存在缺失。
rem
rem 本脚本只读取注册表与文件状态，不安装任何组件，不需要管理员权限。
rem ============================================================
setlocal enabledelayedexpansion

set "WV2KEY1=HKLM\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
set "WV2KEY2=HKLM\SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
set "WV2KEY3=HKCU\Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
set "PF86=%ProgramFiles(x86)%"
set "PF=%ProgramFiles%"
set "MISS=0"

echo Literature Translation —— 依赖检测
echo --------------------------------------------
for /f "delims=" %%V in ('ver') do echo %%V
echo.

rem ---- WebView2 运行时：三处注册表按官方推荐顺序探测 ----
set "WV2="
set "WV2SCOPE="
set "WV2LOC="
call :probe_wv2 "%WV2KEY1%" 机器级
if not defined WV2 call :probe_wv2 "%WV2KEY2%" 机器级
if not defined WV2 call :probe_wv2 "%WV2KEY3%" 用户级

if defined WV2 (
    echo [√] WebView2 运行时   !WV2!（!WV2SCOPE!安装）
    set "WV2EXE="
    if defined WV2LOC set "WV2EXE=!WV2LOC!\!WV2!\msedgewebview2.exe"
    if defined WV2EXE if exist "!WV2EXE!" (
        echo     !WV2EXE!
    ) else (
        echo     [注意] 主文件缺失（可能被安全/清理软件移除）：!WV2EXE!
        echo     可重装引导安装器覆盖修复，详见 README「常见问题」。
        set "MISS=1"
    )
) else (
    echo [×] WebView2 运行时   未安装 —— 主程序界面无法启动
    set "MISS=1"
)

rem ---- 导出浏览器：Edge 优先，Chrome 兜底（精简系统常见"无 Edge 有 Chrome"） ----
set "EDGE="
if exist "!PF86!\Microsoft\Edge\Application\msedge.exe" set "EDGE=!PF86!\Microsoft\Edge\Application\msedge.exe"
if not defined EDGE if exist "!PF!\Microsoft\Edge\Application\msedge.exe" set "EDGE=!PF!\Microsoft\Edge\Application\msedge.exe"
if not defined EDGE call :probe_apppaths "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe" EDGE
if not defined EDGE call :probe_apppaths "HKCU\Software\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe" EDGE

set "CHROME="
if not defined EDGE (
    if exist "!PF86!\Google\Chrome\Application\chrome.exe" set "CHROME=!PF86!\Google\Chrome\Application\chrome.exe"
    if not defined CHROME if exist "!PF!\Google\Chrome\Application\chrome.exe" set "CHROME=!PF!\Google\Chrome\Application\chrome.exe"
    if not defined CHROME call :probe_apppaths "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe" CHROME
    if not defined CHROME call :probe_apppaths "HKCU\Software\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe" CHROME
)

set "BROWSER="
if defined EDGE set "BROWSER=!EDGE!"
if not defined BROWSER if defined CHROME set "BROWSER=!CHROME!"

set "EVER="
if defined BROWSER (
    set "EDGEEXE=!BROWSER!"
    for /f %%V in ('powershell -nop -c "(Get-Item -LiteralPath $env:EDGEEXE).VersionInfo.FileVersion" 2^>nul') do set "EVER=%%V"
)

if defined EDGE (
    if defined EVER (echo [√] Microsoft Edge    !EVER!) else echo [√] Microsoft Edge    已安装（版本未知）
    echo     !EDGE!
) else if defined CHROME (
    if defined EVER (echo [注意] Edge 未找到，已检测到 Chrome !EVER!) else echo [注意] Edge 未找到，已检测到 Chrome
    echo     PDF 导出自动改用 Chrome，无需安装：
    echo     !CHROME!
) else (
    echo [×] Microsoft Edge     未找到（也未检测到 Chrome）—— 仅影响 PDF 导出
    set "MISS=1"
)

echo.
rem 修复步骤与官方下载链接的唯一维护处是 README「常见问题」；Y 键直达该页
set "OPEN_URL=https://github.com/Pet-Max/Literature-Translation#%E5%B8%B8%E8%A7%81%E9%97%AE%E9%A2%98"

if "!MISS!"=="1" (
    echo 结论：存在缺失依赖。修复步骤与官方下载链接见 README「常见问题」：
    echo     github.com/Pet-Max/Literature-Translation#常见问题
) else if defined CHROME (
    echo 结论：依赖齐全（Edge 缺失，PDF 导出将使用 Chrome）。
) else (
    echo 结论：依赖齐全，主程序可正常启动与导出 PDF。
)

if not "!MISS!"=="1" goto :wait_quit
echo.
choice /c YN /n /m "是否打开该页面？[Y/N] "
if errorlevel 2 goto :wait_quit
start "" "!OPEN_URL!"

:wait_quit
echo.
echo %cmdcmdline% | "%SystemRoot%\System32\find.exe" /i "%~nx0" >nul && pause
exit /b !MISS!

:probe_wv2
set "PV="
for /f "tokens=3" %%A in ('reg query "%~1" /v pv 2^>nul ^| findstr /i "pv"') do set "PV=%%A"
if not defined PV goto :eof
if "%PV%"=="0.0.0.0" goto :eof
set "WV2=%PV%"
set "WV2SCOPE=%~2"
for /f "tokens=2*" %%A in ('reg query "%~1" /v location 2^>nul ^| findstr /i "location"') do set "WV2LOC=%%B"
goto :eof

:probe_apppaths
set "APPV="
for /f "tokens=2*" %%A in ('reg query "%~1" /ve 2^>nul ^| findstr /i "REG_SZ"') do set "APPV=%%B"
if not defined APPV goto :eof
set APPV=!APPV:"=!
if exist "!APPV!" set "%~2=!APPV!"
goto :eof
