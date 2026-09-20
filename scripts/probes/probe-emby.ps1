param(
    [Parameter(Mandatory=$true)][string]$BaseUrl,
    [Parameter(Mandatory=$true)][string]$ApiKey,
    [int]$TimeoutSeconds = 10,
    [switch]$NoExitOnFailure
)

$ErrorActionPreference = "Stop"
$base = $BaseUrl.TrimEnd("/")
$headers = @{ "X-Emby-Token" = $ApiKey }

try {
    $response = Invoke-RestMethod -Method Get -Uri "$base/System/Info" -Headers $headers -TimeoutSec $TimeoutSeconds
    [pscustomobject]@{
        ok = $true
        service = "emby"
        version = $response.Version
        server_name = $response.ServerName
        id_present = -not [string]::IsNullOrWhiteSpace([string]$response.Id)
    } | ConvertTo-Json -Depth 4
} catch {
    [pscustomobject]@{
        ok = $false
        service = "emby"
        message = $_.Exception.Message
    } | ConvertTo-Json -Depth 4
    if (-not $NoExitOnFailure) { exit 1 }
}
