$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

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
  return $null
}

$required = @(
  ".gitattributes",
  "go.mod",
  "go.sum",
  "apps/manager/main.go",
  "apps/client/main.go",
  "apps/client/security.go",
  "apps/client/setup_handlers.go",
  "internal/adapters/clouddrive2/client.go",
  "internal/setup/store.go",
  "internal/transfer/service.go",
  "internal/config/config.go",
  "internal/httpapi/health.go",
  "deploy/docker/docker-compose.yml",
  "deploy/docker/docker-compose.client.yml",
  "deploy/docker/docker-compose.manager.yml",
  "deploy/docker/.env.example",
  "deploy/docker/.dockerignore",
  "deploy/docker/manager.Dockerfile",
  "deploy/docker/manager-runtime.Dockerfile",
  "deploy/docker/client-runtime.Dockerfile",
  "packaging/README.md",
  "scripts/release-lib.ps1",
  "scripts/package-archive.go"
)

$missing = $required | Where-Object { -not (Test-Path -LiteralPath $_) }
if ($missing) {
  throw "Missing required files: $($missing -join ', ')"
}

$reserved = @("8096", "8098", "8092", "3333", "3334", "19798")
function Assert-NotReservedPort([string]$Port, [string]$Source) {
  if ($reserved -contains $Port) {
    throw "Reserved listening port $Port appears in $Source"
  }
}

$envText = Get-Content -LiteralPath "deploy/docker/.env.example"
foreach ($line in $envText) {
  if ($line -match "^BOSSTRANSFER_MANAGER_HTTP_PORT=(\d+)$") {
    Assert-NotReservedPort $Matches[1] "deploy/docker/.env.example"
  }
  if ($line -match "^BOSSTRANSFER_MANAGER_ADDR=.*:(\d+)$") {
    Assert-NotReservedPort $Matches[1] "deploy/docker/.env.example"
  }
}

$composePaths = @(
  "deploy/docker/docker-compose.yml",
  "deploy/docker/docker-compose.client.yml",
  "deploy/docker/docker-compose.manager.yml"
)
foreach ($composePath in $composePaths) {
  $compose = Get-Content -LiteralPath $composePath -Raw
  foreach ($match in [regex]::Matches($compose, "\$\{BOSSTRANSFER_MANAGER_HTTP_PORT:-(\d+)\}:(\d+)")) {
    Assert-NotReservedPort $match.Groups[1].Value $composePath
    Assert-NotReservedPort $match.Groups[2].Value $composePath
  }
  if ($compose -match "(?m)^\s+depends_on:\s*$") {
    throw "Client deployment must not depend on a local manager: $composePath"
  }
  if ($compose -match "BOSSTRANSFER_SETUP_PASSWORD:\s*`"\$\{BOSSTRANSFER_SETUP_PASSWORD:\?") {
    throw "Client bootstrap password must not be required by Compose: $composePath"
  }
  $serviceCount = [regex]::Matches($compose, '(?m)^    image:\s+bosstransfer/').Count
  foreach ($guard in @(
    @{Pattern = '(?m)^    read_only:\s+true\s*$'; Name = 'read-only root filesystem'},
    @{Pattern = '(?m)^    pids_limit:\s+128\s*$'; Name = 'PID limit'},
    @{Pattern = '(?m)^      driver:\s+json-file\s*$'; Name = 'bounded json-file logging'},
    @{Pattern = '(?m)^        max-size:\s+"10m"\s*$'; Name = 'log size limit'},
    @{Pattern = '(?m)^        max-file:\s+"3"\s*$'; Name = 'log file count limit'},
    @{Pattern = '(?m)^      -\s+no-new-privileges:true\s*$'; Name = 'no-new-privileges'}
  )) {
    if ([regex]::Matches($compose, $guard.Pattern).Count -ne $serviceCount) {
      throw "Compose service is missing $($guard.Name): $composePath"
    }
  }
}

$dockerignore = Get-Content -LiteralPath "deploy/docker/.dockerignore" -Raw
foreach ($pattern in @(".env", "*.pem", "*.key", "*.log", "docs/", "api/")) {
  if (-not ($dockerignore -split "`r?`n" -contains $pattern)) {
    throw "Docker build context does not ignore $pattern"
  }
}

$composeVolumeDefaults = @{
  "deploy/docker/docker-compose.yml" = @(
    '${BOSSTRANSFER_MANAGER_DATA_VOLUME:-bosstransfer_manager-data}',
    '${BOSSTRANSFER_CLIENT_DATA_VOLUME:-bosstransfer_client-data}'
  )
  "deploy/docker/docker-compose.manager.yml" = @('${BOSSTRANSFER_MANAGER_DATA_VOLUME:-bosstransfer_manager-data}')
  "deploy/docker/docker-compose.client.yml" = @('${BOSSTRANSFER_CLIENT_DATA_VOLUME:-bosstransfer_client-data}')
}
foreach ($entry in $composeVolumeDefaults.GetEnumerator()) {
  $compose = Get-Content -LiteralPath $entry.Key -Raw
  foreach ($volumeName in $entry.Value) {
    if (-not $compose.Contains('name: "' + $volumeName + '"')) {
      throw "Compose no longer reuses the 1.1.0 data volume by default: $($entry.Key) -> $volumeName"
    }
  }
}

$runtimeImages = @{
  "deploy/docker/manager-runtime.Dockerfile" = "manager"
  "deploy/docker/client-runtime.Dockerfile" = "client"
}
foreach ($entry in $runtimeImages.GetEnumerator()) {
  $dockerfile = Get-Content -LiteralPath $entry.Key -Raw
  if ($dockerfile -notmatch '(?m)^FROM alpine:3[.]22[.]6 AS certificates\s*$') {
    throw "Runtime CA stage must use the reviewed Alpine 3.22.6 image: $($entry.Key)"
  }
  if ($dockerfile -notmatch '(?m)^RUN apk add --no-cache ca-certificates\s*$') {
    throw "Runtime image does not install the public CA bundle: $($entry.Key)"
  }
  if ($dockerfile -notmatch '(?m)^COPY --from=certificates /etc/ssl/certs/ca-certificates[.]crt /etc/ssl/certs/ca-certificates[.]crt\s*$') {
    throw "Runtime scratch image does not contain the public CA bundle: $($entry.Key)"
  }
  if ($dockerfile -notmatch ('(?m)^COPY --chmod=755 bin/' + $entry.Value + ' /' + $entry.Value + '\s*$')) {
    throw "Runtime image does not copy the packaged $($entry.Value) binary: $($entry.Key)"
  }
}

$shellAttributes = @(git check-attr eol -- "scripts/remote-smoke.sh")
if ($LASTEXITCODE -ne 0 -or $shellAttributes.Count -ne 1 -or $shellAttributes[0] -notmatch ': eol: lf$') {
  throw "Linux smoke scripts must be checked out with LF line endings"
}
$remoteSmokeBytes = [System.IO.File]::ReadAllBytes((Resolve-Path -LiteralPath "scripts/remote-smoke.sh"))
if ($remoteSmokeBytes -contains 13) {
  throw "scripts/remote-smoke.sh contains CR bytes and will not execute from the Linux release archive"
}
$remoteSmoke = Get-Content -LiteralPath "scripts/remote-smoke.sh" -Raw
if ($remoteSmoke -notmatch 'BOSSTRANSFER_DATA_DIR="\$DATA_DIR"') {
  throw "Remote smoke test must use its writable release-local manager data directory"
}

$makefile = Get-Content -LiteralPath "Makefile" -Raw
foreach ($role in @("manager", "client")) {
  if ($makefile -notmatch ('-o deploy/docker/bin/' + $role + ' ./apps/' + $role)) {
    throw "make docker must build deploy/docker/bin/$role before using the runtime Dockerfile"
  }
}
$gitignore = Get-Content -LiteralPath ".gitignore" -Raw
if ($gitignore -notmatch '(?m)^deploy/docker/bin/\s*$') {
  throw "Source Docker build binaries must be ignored by Git"
}

$configGo = Get-Content -LiteralPath "internal/config/config.go" -Raw
foreach ($match in [regex]::Matches($configGo, 'Default(?:Manager|Client)Addr\s*=\s*"[^"]+:(\d+)"')) {
  Assert-NotReservedPort $match.Groups[1].Value "internal/config/config.go"
}


$trackedEnv = @(git ls-files -- '.env' '.env.*' ':!deploy/docker/.env.example')
if ($LASTEXITCODE -ne 0) {
  throw "git ls-files failed"
}
if ($trackedEnv.Count -ne 0) {
  throw "Sensitive environment files are tracked: $($trackedEnv -join ', ')"
}

$sensitiveMatches = @(git grep -n -I -E -e '-----BEGIN ([A-Z ]+ )?PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|[A-Za-z]:\\Users\\[^\\]+' -- . ':!go.sum')
if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 1) {
  throw "sensitive marker scan failed"
}
if ($sensitiveMatches.Count -ne 0) {
  throw "Sensitive marker found in tracked files: $($sensitiveMatches -join '; ')"
}

$goFiles = Get-ChildItem -Recurse -Filter "*.go" | Where-Object { $_.FullName -notmatch "\\(vendor|\.cache|\.tools|dist)\\" }
foreach ($file in $goFiles) {
  $text = Get-Content -LiteralPath $file.FullName -Raw
  if ($text -match "TODO|panic\(") {
    throw "Unexpected TODO or panic in $($file.FullName)"
  }
}

$goExe = Find-Go
$goStatus = "skipped"
$buildStatus = "skipped"
if ($goExe) {
  $workspace = Split-Path -Parent $root
  $env:GOTOOLCHAIN = "local"
  $env:GOCACHE = Join-Path $workspace ".cache\go-build"
  $env:GOMODCACHE = Join-Path $workspace ".cache\go-mod"
  New-Item -ItemType Directory -Force -Path $env:GOCACHE,$env:GOMODCACHE | Out-Null
  & $goExe test ./...
  if ($LASTEXITCODE -ne 0) {
    throw "go test failed"
  }
  $goStatus = "passed"
  New-Item -ItemType Directory -Force -Path "dist" | Out-Null
  & $goExe build -trimpath -o dist\manager.exe ./apps/manager
  if ($LASTEXITCODE -ne 0) {
    throw "manager build failed"
  }
  & $goExe build -trimpath -o dist\client.exe ./apps/client
  if ($LASTEXITCODE -ne 0) {
    throw "client build failed"
  }
  $buildStatus = "passed"
}

[pscustomobject]@{
  ok = $true
  checked_files = $required.Count
  go_files = @($goFiles).Count
  go_test = $goStatus
  go_build = $buildStatus
  note = "Structural checks passed. Go test/build run when Go is available. Docker runtime is verified by scripts/build-docker-package.ps1 and target NAS smoke tests."
} | ConvertTo-Json -Depth 3
