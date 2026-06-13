@echo off
echo Building Frontend...
cd frontend
call npm run build
if %errorlevel% neq 0 exit /b %errorlevel%
cd ..

echo Clearing static assets...
if exist pkg\server\web\static rmdir /s /q pkg\server\web\static
mkdir pkg\server\web\static
echo Copying new assets...
xcopy /E /I /Y frontend\dist\* pkg\server\web\static\

echo Building Go Binary...
set SHORT_SHA=unknown
for /f "tokens=*" %%i in ('git rev-parse --short HEAD 2^>nul') do set SHORT_SHA=%%i
set RELEASE_VERSION=0.0.0
for /f "tokens=*" %%v in ('powershell -NoProfile -Command "try { $m=[regex]::Match((Get-Content .release-please-manifest.json -Raw), \"[0-9]+\.[0-9]+\.[0-9]+\"); if($m.Success){$m.Value}else{\"0.0.0\"} } catch { \"0.0.0\" }"') do set RELEASE_VERSION=%%v
go test ./pkg/... -v
go build -ldflags="-X main.Version=%RELEASE_VERSION%-%SHORT_SHA%" ./cmd/seedstream/
if %errorlevel% neq 0 exit /b %errorlevel%

echo Build Complete!
