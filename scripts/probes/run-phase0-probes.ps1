param(
    [string]$EmbyBaseUrl,
    [string]$EmbyApiKey,
    [string]$NextEmbyBaseUrl = "http://127.0.0.1:8091",
    [string]$NextFindBaseUrl = "http://127.0.0.1:8092",
    [string]$CloudDrive2BaseUrl = "http://127.0.0.1:19798"
)

$ErrorActionPreference = "Continue"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$results = @()

if ($EmbyBaseUrl -and $EmbyApiKey) {
    $results += & "$root\probe-emby.ps1" -BaseUrl $EmbyBaseUrl -ApiKey $EmbyApiKey -NoExitOnFailure | ConvertFrom-Json
} else {
    $results += [pscustomobject]@{ ok = $false; service = "emby"; skipped = $true; message = "Set EmbyBaseUrl and EmbyApiKey to probe Emby." }
}

$results += & "$root\probe-nextemby.ps1" -BaseUrl $NextEmbyBaseUrl -NoExitOnFailure | ConvertFrom-Json
$results += & "$root\probe-nextfind.ps1" -BaseUrl $NextFindBaseUrl -NoExitOnFailure | ConvertFrom-Json
$results += & "$root\probe-clouddrive2.ps1" -BaseUrl $CloudDrive2BaseUrl -NoExitOnFailure | ConvertFrom-Json
$results += & "$root\probe-source115.ps1" -NoExitOnFailure | ConvertFrom-Json

$results | ConvertTo-Json -Depth 8
