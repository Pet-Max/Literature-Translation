@echo off
REM prepare_runtime.bat - 一次性准备便携 Python 运行时（产物 build/runtime/，不入库）。
REM 布局遵守 FindPython 约定：exe 旁 runtime\python.exe。
REM 内容：嵌入式 Python 3.12 + pip + pymupdf + httpx（预装好，用户零配置）。
setlocal
cd /d "%~dp0.."
set PYVER=3.12.10
set RUNTIME=build\runtime

if exist "%RUNTIME%\python.exe" (
    echo [skip] %RUNTIME%\python.exe 已就绪
    exit /b 0
)

echo [1/4] 下载嵌入式 Python %PYVER% ...
if not exist "%RUNTIME%" mkdir "%RUNTIME%"
curl -sL --retry 3 -o "%RUNTIME%\python-embed.zip" "https://www.python.org/ftp/python/%PYVER%/python-%PYVER%-embed-amd64.zip"
if not exist "%RUNTIME%\python-embed.zip" exit /b 1
powershell -NoProfile -Command "Expand-Archive -Force '%RUNTIME%\python-embed.zip' '%RUNTIME%'"
del "%RUNTIME%\python-embed.zip"

echo [2/4] 启用 site-packages ...
powershell -NoProfile -Command "(Get-Content '%RUNTIME%\python312._pth') -replace '#\s*import site','import site' | Set-Content '%RUNTIME%\python312._pth'"
>>"%RUNTIME%\python312._pth" echo Lib/site-packages

echo [3/4] 引导 pip ...
curl -sL --retry 3 -o "%RUNTIME%\get-pip.py" https://bootstrap.pypa.io/get-pip.py
"%RUNTIME%\python.exe" "%RUNTIME%\get-pip.py" --no-warn-script-location --quiet
del "%RUNTIME%\get-pip.py"

echo [4/4] 安装 pymupdf + httpx ...
"%RUNTIME%\python.exe" -m pip install --quiet --no-warn-script-location pymupdf httpx
"%RUNTIME%\python.exe" -c "import fitz, httpx; print('runtime OK, httpx', httpx.__version__)"
if errorlevel 1 exit /b 1
echo runtime ready: %RUNTIME%
