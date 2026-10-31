@echo off
REM check.bat - one-shot health check: static analysis + Python unit tests
REM + Go tests + frontend E2E. "go test ./..." includes unit tests, fuzz seed
REM corpus and the pipeline E2E (real Python sidecar + mock LLM; auto-skipped
REM when python/Edge is missing).
setlocal
cd /d "%~dp0.."
set FAILED=0

echo [1/6] go vet (Go static analysis)...
go vet ./...
if errorlevel 1 set FAILED=1

echo.
echo [2/6] workflow lint (bash/pwsh syntax consistency)...
python .github/scripts/lint_workflows.py
if errorlevel 1 set FAILED=1

echo.
echo [3/6] ruff check engine/ (Python static analysis)...
python -m ruff --version >nul 2>nul
if errorlevel 1 (
    echo   [skip] ruff not installed - pip install ruff
) else (
    python -m ruff check engine/
    if errorlevel 1 set FAILED=1
)

echo.
echo [4/6] python unittest engine/tests (engine pure-logic tests)...
python -m unittest discover -s engine/tests
if errorlevel 1 set FAILED=1

echo.
echo [5/6] go test ./... (unit + fuzz seeds + pipeline E2E)...
go test ./...
if errorlevel 1 set FAILED=1

echo.
echo [6/6] frontend E2E (node tests/e2e_frontend.mjs)...
where node >nul 2>nul
if errorlevel 1 (
    echo   [skip] node not installed
) else (
    node tests/e2e_frontend.mjs
    if errorlevel 1 set FAILED=1
)

echo.
if %FAILED%==1 (
    echo X health check FAILED, see output above
    exit /b 1
)
echo OK - all checks passed
exit /b 0
