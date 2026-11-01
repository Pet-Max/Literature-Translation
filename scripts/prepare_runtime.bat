@echo off
REM prepare_runtime.bat - one-shot portable Python runtime builder (output build/runtime/, not committed).
REM Layout follows the FindPython convention: runtime\python.exe next to the main exe.
REM Contents: embedded Python 3.12 + pymupdf==1.26.7 + httpx==0.28.1 (preinstalled, zero user setup).
REM pymupdf pinned to 1.x: since 2.0 it hard-pulls numpy/onnxruntime/pymupdf-layout etc.
REM (~200M dead weight); the engine only uses the fitz core for PDF text extraction.
REM Build-time leftovers (pip, Scripts, __pycache__) are removed after install; delete build\runtime to rebuild.
REM NOTE: keep this file ASCII-only - cmd parses it with the OEM codepage (GBK on zh-CN systems),
REM UTF-8 Chinese comments corrupt line parsing (stray "&" splits lines into bogus commands).
setlocal
cd /d "%~dp0.."
set PYVER=3.12.10
set RUNTIME=build\runtime

if exist "%RUNTIME%\python.exe" (
    echo [skip] %RUNTIME%\python.exe already present
    exit /b 0
)

echo [1/5] downloading embedded Python %PYVER% ...
if not exist "%RUNTIME%" mkdir "%RUNTIME%"
curl -sL --retry 3 -o "%RUNTIME%\python-embed.zip" "https://www.python.org/ftp/python/%PYVER%/python-%PYVER%-embed-amd64.zip"
if not exist "%RUNTIME%\python-embed.zip" exit /b 1
powershell -NoProfile -Command "Expand-Archive -Force '%RUNTIME%\python-embed.zip' '%RUNTIME%'"
del "%RUNTIME%\python-embed.zip"

echo [2/5] enabling site-packages ...
powershell -NoProfile -Command "(Get-Content '%RUNTIME%\python312._pth') -replace '#\s*import site','import site' | Set-Content '%RUNTIME%\python312._pth'"
>>"%RUNTIME%\python312._pth" echo Lib/site-packages

echo [3/5] bootstrapping pip ...
curl -sL --retry 3 -o "%RUNTIME%\get-pip.py" https://bootstrap.pypa.io/get-pip.py
"%RUNTIME%\python.exe" "%RUNTIME%\get-pip.py" --no-warn-script-location --quiet
del "%RUNTIME%\get-pip.py"

echo [4/5] installing pymupdf==1.26.7 + httpx==0.28.1 ...
"%RUNTIME%\python.exe" -m pip install --quiet --no-warn-script-location pymupdf==1.26.7 httpx==0.28.1
"%RUNTIME%\python.exe" -c "import fitz, httpx; print('runtime OK, httpx', httpx.__version__)"
if errorlevel 1 exit /b 1

echo [5/5] removing build-time tools and caches ...
if exist "%RUNTIME%\Lib\site-packages\pip" rmdir /s /q "%RUNTIME%\Lib\site-packages\pip"
for /d %%d in ("%RUNTIME%\Lib\site-packages\pip-*.dist-info") do rmdir /s /q "%%d"
if exist "%RUNTIME%\Scripts" rmdir /s /q "%RUNTIME%\Scripts"
if exist "%RUNTIME%\Lib\site-packages\pymupdf\mupdf-devel" rmdir /s /q "%RUNTIME%\Lib\site-packages\pymupdf\mupdf-devel"
if exist "%RUNTIME%\Lib\site-packages\pymupdf\layout" rmdir /s /q "%RUNTIME%\Lib\site-packages\pymupdf\layout"
powershell -NoProfile -Command "Get-ChildItem -Path '%RUNTIME%' -Recurse -Directory -Filter '__pycache__' -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force"
"%RUNTIME%\python.exe" -B -c "import fitz, httpx; print('post-clean OK')"
if errorlevel 1 exit /b 1
echo runtime ready: %RUNTIME%
