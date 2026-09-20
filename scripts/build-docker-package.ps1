param(
  [ValidateSet("amd64", "arm64")]
  [string]$Architecture = "amd64"
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
. (Join-Path $PSScriptRoot "release-lib.ps1")

function Find-Go {
  $localCandidates = @(
    (Join-Path $root ".tools\go-dist\go\bin\go.exe"),
    (Join-Path (Split-Path -Parent $root) ".tools\go1.27.1\go\bin\go.exe")
  )
  foreach ($localGo in $localCandidates) {
    if (Test-Path -LiteralPath $localGo) {
      return (Resolve-Path -LiteralPath $localGo).Path
    }
  }
  $cmd = Get-Command go -ErrorAction SilentlyContinue
  if ($cmd) {
    return $cmd.Source
  }
  throw "Go was not found. Run setup or install Go first."
}

function Copy-RequiredFile([string]$Source, [string]$Destination) {
  if (-not (Test-Path -LiteralPath $Source)) {
    throw "Missing required file: $Source"
  }
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Destination) | Out-Null
  Copy-Item -LiteralPath $Source -Destination $Destination -Force
}

$goExe = Find-Go
$workspace = Split-Path -Parent $root
$hostGOOS = (& $goExe env GOOS).Trim()
$hostGOARCH = (& $goExe env GOARCH).Trim()
$env:GOTOOLCHAIN = "local"
$env:GOCACHE = Join-Path $workspace ".cache\go-build"
$env:GOMODCACHE = Join-Path $workspace ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE,$env:GOMODCACHE | Out-Null

$version = (Get-Content -LiteralPath "VERSION" -Raw).Trim()
Assert-CleanReleaseTag $version

& $goExe test ./...
if ($LASTEXITCODE -ne 0) {
  throw "go test failed"
}

$dockerRoot = Join-Path $root "dist\docker"
$stageName = "bosstransfer-docker-$version-linux-$Architecture"
$stage = Join-Path $dockerRoot $stageName
if (Test-Path -LiteralPath $stage) {
  Remove-Item -LiteralPath $stage -Recurse -Force
}
New-Item -ItemType Directory -Force -Path (Join-Path $stage "bin"),(Join-Path $stage "docs") | Out-Null

$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = $Architecture
& $goExe build -trimpath -ldflags="-s -w" -o (Join-Path $stage "bin\manager") ./apps/manager
if ($LASTEXITCODE -ne 0) { throw "manager linux/$Architecture build failed" }
& $goExe build -trimpath -ldflags="-s -w" -o (Join-Path $stage "bin\client") ./apps/client
if ($LASTEXITCODE -ne 0) { throw "client linux/$Architecture build failed" }

Copy-RequiredFile "deploy\docker\docker-compose.yml" (Join-Path $stage "docker-compose.yml")
Copy-RequiredFile "deploy\docker\docker-compose.client.yml" (Join-Path $stage "docker-compose.client.yml")
Copy-RequiredFile "deploy\docker\docker-compose.manager.yml" (Join-Path $stage "docker-compose.manager.yml")
Write-OfflineRuntimeFiles $stage
Copy-RequiredFile "deploy\docker\.env.example" (Join-Path $stage ".env.example")
Copy-RequiredFile "deploy\docker\.dockerignore" (Join-Path $stage ".dockerignore")
Copy-RequiredFile "VERSION" (Join-Path $stage "VERSION")
Copy-RequiredFile "README.md" (Join-Path $stage "README.md")
Copy-RequiredFile "CHANGELOG.md" (Join-Path $stage "CHANGELOG.md")
Copy-RequiredFile "api\openapi.yaml" (Join-Path $stage "api\openapi.yaml")
Copy-RequiredFile "docs\security-model.md" (Join-Path $stage "docs\security-model.md")
Copy-RequiredFile "docs\architecture.md" (Join-Path $stage "docs\architecture.md")
Copy-RequiredFile "docs\api-compatibility.md" (Join-Path $stage "docs\api-compatibility.md")
Copy-RequiredFile "docs\deployment-production.md" (Join-Path $stage "docs\deployment-production.md")
Copy-RequiredFile "docs\customer-operation-guide.md" (Join-Path $stage "docs\customer-operation-guide.md")
Copy-RequiredFile "docs\github-upload-guide.md" (Join-Path $stage "docs\github-upload-guide.md")
Copy-RequiredFile "docs\legacy-migration.md" (Join-Path $stage "docs\legacy-migration.md")

$readmeLines = @(
  "# BossTransfer Docker production package",
  "",
  "Version: $version",
  "Architecture: linux/$Architecture",
  "",
  "## Configure",
  "",
  "Create the private configuration file, restrict it to its owner, then edit it:",
  "",
  "cp .env.example .env",
  "chmod 600 .env",
  "",
  "BOSSTRANSFER_MANAGER_HTTP_PORT=18085",
  "BOSSTRANSFER_CLIENT_HTTP_PORT=18086",
  "BOSSTRANSFER_CD2_ADDR=host.docker.internal:19798",
  "BOSSTRANSFER_SETUP_USER=admin",
  "BOSSTRANSFER_SETUP_PASSWORD=password",
  "BOSSTRANSFER_MANAGER_URL=",
  "BOSSTRANSFER_LOCAL_DOWNLOAD_DIR=./data/downloads",
  "",
  "The environment Web credentials bootstrap a new data volume only. Later changes do not overwrite credentials changed in the Web UI.",
  "",
  "## Start",
  "",
  "The package contains its CA bundle and scratch runtime Dockerfiles, so these image builds do not pull a registry base image.",
  "",
  "Manager: docker compose -f docker-compose.manager.yml -p bosstransfer-manager build --no-cache; then run docker compose -f docker-compose.manager.yml -p bosstransfer-manager up -d",
  "Client: docker compose -f docker-compose.client.yml -p bosstransfer-client build --no-cache; then run docker compose -f docker-compose.client.yml -p bosstransfer-client up -d",
  "Combined: docker compose -f docker-compose.yml -p bosstransfer build --no-cache; then run docker compose -f docker-compose.yml -p bosstransfer up -d",
  "",
  "## First connection",
  "",
  "1. Sign in to each new Web service with admin / password and change it in the UI.",
  "2. On the manager, enter the 115 Open AppID, scan the QR code with the owner account, and select the searchable resource directory.",
  "3. Create a customer, set its expiry and device limit, then issue a one-time activation code.",
  "4. On the client, enter the manager URL and code to activate this installation, then bind the customer's own 115 account.",
  "5. Select the local system target. CloudDrive2 and qBittorrent appear as usable choices only after the customer configures and verifies them locally.",
  "",
  "## Open in browser",
  "",
  "- Manager: http://<NAS-IP>:18085/",
  "- Client: http://<NAS-IP>:18086/",
  "",
  "## Verify",
  "",
  "docker compose -f docker-compose.client.yml -p bosstransfer-client ps",
  "curl -fsS http://127.0.0.1:18086/api/v1/health/ready",
  "",
  "Preserve bosstransfer_manager-data and bosstransfer_client-data across upgrades. Never use docker compose down -v."
)
[string]::Join([Environment]::NewLine, $readmeLines) | Set-Content -LiteralPath (Join-Path $stage "README-docker.md") -Encoding UTF8

$archive = Join-Path $dockerRoot "$stageName.tar.gz"
if (Test-Path -LiteralPath $archive) {
  Remove-Item -LiteralPath $archive -Force
}
$env:GOOS = $hostGOOS
$env:GOARCH = $hostGOARCH
& $goExe run .\scripts\package-archive.go -source $dockerRoot -root $stageName -output $archive -executable "bin/manager,bin/client"
if ($LASTEXITCODE -ne 0) {
  throw "archive packaging failed for $stageName"
}

$hash = Get-FileHash -LiteralPath $archive -Algorithm SHA256
$manifest = [pscustomobject]@{
  ok = $true
  version = $version
  arch = $Architecture
  package_dir = $stageName
  archive = (Split-Path -Leaf $archive)
  sha256 = $hash.Hash.ToLowerInvariant()
  bytes = (Get-Item -LiteralPath $archive).Length
}
$manifest | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $dockerRoot "docker-manifest.json") -Encoding UTF8
$manifest | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $dockerRoot "docker-manifest-$Architecture.json") -Encoding UTF8
$manifest | ConvertTo-Json -Depth 4
