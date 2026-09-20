param(
    [string]$BaseUrl = "http://127.0.0.1:8091",
    [int]$TimeoutSeconds = 10,
    [switch]$NoExitOnFailure
)

$ErrorActionPreference = "Stop"
$base = $BaseUrl.TrimEnd("/")
$paths = @("/health", "/api/health", "/version", "/")
$results = @()

foreach ($path in $paths) {
    try {
        $response = Invoke-WebRequest -Method Get -Uri "$base$path" -TimeoutSec $TimeoutSeconds -UseBasicParsing
        $results += [pscustomobject]@{
            path = $path
            status = [int]$response.StatusCode
            content_type = $response.Headers["Content-Type"]
        }
    } catch {
        $results += [pscustomobject]@{
            path = $path
            status = 0
            error = $_.Exception.Message
        }
    }
}

$ok = $results | Where-Object { $_.status -ge 200 -and $_.status -lt 500 } | Select-Object -First 1
[pscustomobject]@{
    ok = $null -ne $ok
    service = "nextemby"
    base_url = $base
    probes = $results
    note = "No stable public API has been verified yet; this is reachability only."
} | ConvertTo-Json -Depth 5

if ($null -eq $ok -and -not $NoExitOnFailure) { exit 1 }
