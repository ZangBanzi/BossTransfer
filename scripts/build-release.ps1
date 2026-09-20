param(
  [ValidateSet("amd64", "arm64")]
  [string[]]$Architectures = @("amd64", "arm64")
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
  throw "Go was not found. Install Go or use the bundled toolchain."
}

function Copy-ReleaseFile([string]$Source, [string]$Destination) {
  if (-not (Test-Path -LiteralPath $Source)) {
    throw "Missing required file: $Source"
  }
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Destination) | Out-Null
  Copy-Item -LiteralPath $Source -Destination $Destination -Force
}

function Copy-ReleaseCompose([string]$Source, [string]$RootDestination, [string]$DeployDestination) {
  Copy-ReleaseFile $Source $RootDestination
  Copy-ReleaseFile $Source $DeployDestination

  # The duplicate Compose files under deploy/ run with deploy/ as their
  # project directory. Their image build context must point at the release
  # root, where bin/ and the runtime Dockerfiles are packaged.
  $compose = Get-Content -LiteralPath $DeployDestination -Raw
  $deployCompose = $compose -replace '(?m)^(\s+context:\s*)[.]\s*$', '${1}..'
  if ($deployCompose -eq $compose) {
    throw "Compose build context was not rewritten for $DeployDestination"
  }
  [System.IO.File]::WriteAllText($DeployDestination, $deployCompose, [System.Text.UTF8Encoding]::new($false))
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

$testOutput = & $goExe test ./... 2>&1
if ($LASTEXITCODE -ne 0) {
  $testOutput | ForEach-Object { Write-Error $_ }
  throw "go test failed"
}

$releaseRoot = Join-Path $root "dist\release"
New-Item -ItemType Directory -Force -Path $releaseRoot | Out-Null
$manifest = @()

foreach ($arch in $Architectures) {
  $stageName = "bosstransfer-$version-linux-$arch"
  $stage = Join-Path $releaseRoot $stageName
  if (Test-Path -LiteralPath $stage) {
    Remove-Item -LiteralPath $stage -Recurse -Force
  }
  New-Item -ItemType Directory -Force -Path (Join-Path $stage "bin"),(Join-Path $stage "docs"),(Join-Path $stage "api"),(Join-Path $stage "deploy") | Out-Null

  $env:CGO_ENABLED = "0"
  $env:GOOS = "linux"
  $env:GOARCH = $arch
  & $goExe build -trimpath -ldflags="-s -w" -o (Join-Path $stage "bin\manager") ./apps/manager
  if ($LASTEXITCODE -ne 0) { throw "manager linux/$arch build failed" }
  & $goExe build -trimpath -ldflags="-s -w" -o (Join-Path $stage "bin\client") ./apps/client
  if ($LASTEXITCODE -ne 0) { throw "client linux/$arch build failed" }

  Copy-ReleaseFile "README.md" (Join-Path $stage "README.md")
  Copy-ReleaseFile "VERSION" (Join-Path $stage "VERSION")
  Copy-ReleaseFile "CHANGELOG.md" (Join-Path $stage "CHANGELOG.md")
  Copy-ReleaseFile "api\openapi.yaml" (Join-Path $stage "api\openapi.yaml")
  Copy-ReleaseCompose "deploy\docker\docker-compose.yml" (Join-Path $stage "docker-compose.yml") (Join-Path $stage "deploy\docker-compose.yml")
  Copy-ReleaseCompose "deploy\docker\docker-compose.client.yml" (Join-Path $stage "docker-compose.client.yml") (Join-Path $stage "deploy\docker-compose.client.yml")
  Copy-ReleaseCompose "deploy\docker\docker-compose.manager.yml" (Join-Path $stage "docker-compose.manager.yml") (Join-Path $stage "deploy\docker-compose.manager.yml")
  Write-OfflineRuntimeFiles $stage
  Copy-ReleaseFile "deploy\docker\.env.example" (Join-Path $stage "deploy\.env.example")
  Copy-ReleaseFile "deploy\docker\.env.example" (Join-Path $stage ".env.example")
  Copy-ReleaseFile "docs\api-compatibility.md" (Join-Path $stage "docs\api-compatibility.md")
  Copy-ReleaseFile "docs\architecture.md" (Join-Path $stage "docs\architecture.md")
  Copy-ReleaseFile "docs\security-model.md" (Join-Path $stage "docs\security-model.md")
  Copy-ReleaseFile "docs\deployment-production.md" (Join-Path $stage "docs\deployment-production.md")
  Copy-ReleaseFile "docs\customer-operation-guide.md" (Join-Path $stage "docs\customer-operation-guide.md")
  Copy-ReleaseFile "docs\github-upload-guide.md" (Join-Path $stage "docs\github-upload-guide.md")
  Copy-ReleaseFile "docs\legacy-migration.md" (Join-Path $stage "docs\legacy-migration.md")
  Copy-ReleaseFile "scripts\remote-smoke.sh" (Join-Path $stage "remote-smoke.sh")

  $archive = Join-Path $releaseRoot "$stageName.tar.gz"
  if (Test-Path -LiteralPath $archive) {
    Remove-Item -LiteralPath $archive -Force
  }
  $env:GOOS = $hostGOOS
  $env:GOARCH = $hostGOARCH
  & $goExe run .\scripts\package-archive.go -source $releaseRoot -root $stageName -output $archive -executable "bin/manager,bin/client,remote-smoke.sh"
  if ($LASTEXITCODE -ne 0) { throw "tar failed for $stageName" }

  $hash = Get-FileHash -LiteralPath $archive -Algorithm SHA256
  $manifest += [pscustomobject]@{
    name = Split-Path -Leaf $archive
    arch = $arch
    sha256 = $hash.Hash.ToLowerInvariant()
    bytes = (Get-Item -LiteralPath $archive).Length
  }
}

$manifestPath = Join-Path $releaseRoot "release-manifest.json"
ConvertTo-Json -InputObject @($manifest) -Depth 4 | Set-Content -LiteralPath $manifestPath -Encoding UTF8

[pscustomobject]@{
  ok = $true
  version = $version
  release_dir = $releaseRoot
  artifacts = $manifest
} | ConvertTo-Json -Depth 5
