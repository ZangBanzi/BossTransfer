$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
. (Join-Path $PSScriptRoot "release-lib.ps1")

$version = (Get-Content -LiteralPath "VERSION" -Raw).Trim()
Assert-CleanReleaseTag $version
$outDir = Join-Path $root "dist\source"
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$archive = Join-Path $outDir "bosstransfer-$version-source.tar.gz"
if (Test-Path -LiteralPath $archive) {
  Remove-Item -LiteralPath $archive -Force
}

git archive --format=tar.gz --prefix="bosstransfer-$version/" -o $archive "v$version"
if ($LASTEXITCODE -ne 0) {
  throw "git archive failed"
}

$hash = Get-FileHash -LiteralPath $archive -Algorithm SHA256
[pscustomobject]@{
  ok = $true
  version = $version
  archive = $archive
  sha256 = $hash.Hash.ToLowerInvariant()
  bytes = (Get-Item -LiteralPath $archive).Length
} | ConvertTo-Json -Depth 4
