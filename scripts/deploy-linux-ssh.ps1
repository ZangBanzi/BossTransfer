param(
  [Parameter(Mandatory=$true)][string]$HostName,
  [string]$UserName = "",
  [int]$Port = 22,
  [ValidateSet("amd64", "arm64")][string]$Architecture = "amd64",
  [string]$RemoteDir = '$HOME/bosstransfer',
  [string]$IdentityFile = "",
  [int]$ManagerPort = 8085
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function SshTarget {
  if ($UserName.Trim() -eq "") { return $HostName }
  return "$UserName@$HostName"
}

function SshArgs {
  $args = @("-p", [string]$Port, "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2")
  if ($IdentityFile.Trim() -ne "") {
    $args += @("-i", $IdentityFile)
  }
  return $args
}

function ScpArgs {
  $args = @("-P", [string]$Port, "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2")
  if ($IdentityFile.Trim() -ne "") {
    $args += @("-i", $IdentityFile)
  }
  return $args
}

$ssh = Get-Command ssh -ErrorAction Stop
$scp = Get-Command scp -ErrorAction Stop

$build = powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root "scripts\build-release.ps1") -Architectures $Architecture | ConvertFrom-Json
if (-not $build.ok) {
  throw "build-release did not report ok"
}

$artifact = $build.artifacts | Where-Object { $_.arch -eq $Architecture } | Select-Object -First 1
if (-not $artifact) {
  throw "artifact for $Architecture was not produced"
}
$artifactPath = Join-Path $build.release_dir $artifact.name
$remoteArchive = "/tmp/$($artifact.name)"
$target = SshTarget
$sshArgs = SshArgs
$scpArgs = ScpArgs

& $scp.Source @scpArgs $artifactPath "${target}:$remoteArchive"
if ($LASTEXITCODE -ne 0) { throw "scp upload failed" }

$remoteScript = @"
set -eu
mkdir -p "$RemoteDir"
tar -xzf "$remoteArchive" -C "$RemoteDir" --strip-components=1
cd "$RemoteDir"
chmod +x ./bin/manager ./bin/client ./remote-smoke.sh
BOSSTRANSFER_MANAGER_HTTP_PORT="$ManagerPort" ./remote-smoke.sh
"@

& $ssh.Source @sshArgs $target $remoteScript
if ($LASTEXITCODE -ne 0) { throw "remote smoke failed" }

[pscustomobject]@{
  ok = $true
  target = $HostName
  architecture = $Architecture
  remote_dir = $RemoteDir
  manager_port = $ManagerPort
  artifact = $artifact.name
  sha256 = $artifact.sha256
} | ConvertTo-Json -Depth 4

