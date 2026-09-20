param(
    [string]$BaseUrl = "http://127.0.0.1:19798",
    [int]$TimeoutSeconds = 10,
    [switch]$NoExitOnFailure
)

$ErrorActionPreference = "Stop"
$base = $BaseUrl.TrimEnd("/")

try {
    $response = Invoke-WebRequest -Method Get -Uri "$base/" -TimeoutSec $TimeoutSeconds -UseBasicParsing
    [pscustomobject]@{
        ok = $true
        service = "clouddrive2"
        base_url = $base
        status = [int]$response.StatusCode
        content_type = $response.Headers["Content-Type"]
        note = "HTTP reachability only. gRPC capability verification requires generated client from official proto."
    } | ConvertTo-Json -Depth 4
} catch {
    [pscustomobject]@{
        ok = $false
        service = "clouddrive2"
        base_url = $base
        message = $_.Exception.Message
    } | ConvertTo-Json -Depth 4
    if (-not $NoExitOnFailure) { exit 1 }
}
