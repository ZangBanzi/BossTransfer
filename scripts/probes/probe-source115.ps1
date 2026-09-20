param(
    [string]$OpenPlatformUrl = "https://open.115.com/",
    [int]$TimeoutSeconds = 10,
    [switch]$NoExitOnFailure
)

$ErrorActionPreference = "Stop"

try {
    $response = Invoke-WebRequest -Method Get -Uri $OpenPlatformUrl -TimeoutSec $TimeoutSeconds -UseBasicParsing
    [pscustomobject]@{
        ok = $true
        service = "source115"
        status = [int]$response.StatusCode
        note = "Open platform reachability only. Share API verification requires approved developer app credentials."
    } | ConvertTo-Json -Depth 4
} catch {
    [pscustomobject]@{
        ok = $false
        service = "source115"
        message = $_.Exception.Message
    } | ConvertTo-Json -Depth 4
    if (-not $NoExitOnFailure) { exit 1 }
}
