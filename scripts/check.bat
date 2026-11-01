@echo off
REM check.bat - one-shot health check: static analysis + Go tests (incl.
REM pipeline E2E with in-process Go engine + mock LLM) + frontend E2E.
REM v1.2.0: translation engine is pure Go - no Python/ruff steps anymore.
setlocal
cd /d "%~dp0.."
set FAILED=0

echo [1/4] go vet (Go static analysis)...
go vet ./...
if errorlevel 1 set FAILED=1

echo.
echo [2/4] workflow lint (bash/pwsh syntax consistency)...
python .github/scripts/lint_workflows.py
if errorlevel 1 set FAILED=1

echo.
echo [3/4] go test ./... (unit + fuzz seeds + pipeline E2E)...
go test ./...
if errorlevel 1 set FAILED=1

echo.
echo [4/4] frontend E2E (node tests/e2e_frontend.mjs)...
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
