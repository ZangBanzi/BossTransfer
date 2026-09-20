$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
Add-Type -AssemblyName System.Net.Http

$reservedPorts = @(8092, 8096, 8098, 3333, 3334, 19798)

function Get-FreePort {
  do {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
      $port = [int]$listener.LocalEndpoint.Port
    } finally {
      $listener.Stop()
    }
  } while ($reservedPorts -contains $port)
  return $port
}

function Start-BossProcess([string]$ExePath, [hashtable]$Environment) {
  $psi = [System.Diagnostics.ProcessStartInfo]::new()
  $psi.FileName = (Resolve-Path -LiteralPath $ExePath).Path
  $psi.WorkingDirectory = $root
  $psi.UseShellExecute = $false
  $psi.CreateNoWindow = $true
  foreach ($key in $Environment.Keys) {
    $psi.Environment[$key] = [string]$Environment[$key]
  }
  $process = [System.Diagnostics.Process]::Start($psi)
  if (-not $process) {
    throw "failed to start $ExePath"
  }
  return $process
}

function Stop-BossProcess($Process) {
  if ($Process -and -not $Process.HasExited) {
    $Process.Kill()
    $Process.WaitForExit(5000) | Out-Null
  }
}

function Wait-JsonEndpoint([string]$Uri) {
  $deadline = [DateTime]::UtcNow.AddSeconds(15)
  $lastError = ""
  do {
    try {
      return Invoke-RestMethod -Method Get -Uri $Uri -TimeoutSec 2
    } catch {
      $lastError = $_.Exception.Message
      Start-Sleep -Milliseconds 200
    }
  } while ([DateTime]::UtcNow -lt $deadline)
  throw "endpoint did not become ready: $Uri. Last error: $lastError"
}

function Wait-PageEndpoint([string]$Uri, [string]$MustContain) {
  $deadline = [DateTime]::UtcNow.AddSeconds(15)
  do {
    try {
      $response = Invoke-WebRequest -UseBasicParsing -Method Get -Uri $Uri -TimeoutSec 2
      if ($response.StatusCode -eq 200 -and $response.Content.Contains($MustContain)) {
        return
      }
    } catch {
      Start-Sleep -Milliseconds 200
    }
  } while ([DateTime]::UtcNow -lt $deadline)
  throw "endpoint did not return expected page: $Uri"
}

function New-ApiSession([string]$BaseUrl) {
  $handler = [System.Net.Http.HttpClientHandler]::new()
  $handler.UseCookies = $true
  $handler.CookieContainer = [System.Net.CookieContainer]::new()
  $client = [System.Net.Http.HttpClient]::new($handler)
  $client.Timeout = [TimeSpan]::FromSeconds(10)
  return [pscustomobject]@{
    BaseUrl = $BaseUrl.TrimEnd("/")
    Handler = $handler
    Client = $client
  }
}

function Remove-ApiSession($Session) {
  if ($Session) {
    $Session.Client.Dispose()
    $Session.Handler.Dispose()
  }
}

function Invoke-JsonSession(
  $Session,
  [string]$Method,
  [string]$Path,
  $Body = $null,
  [string]$Origin = ""
) {
  $request = [System.Net.Http.HttpRequestMessage]::new(
    [System.Net.Http.HttpMethod]::new($Method),
    $Session.BaseUrl + $Path
  )
  try {
    $request.Headers.TryAddWithoutValidation("Accept", "application/json") | Out-Null
    if (-not [string]::IsNullOrWhiteSpace($Origin)) {
      $request.Headers.TryAddWithoutValidation("Origin", $Origin) | Out-Null
    }
    if ($null -ne $Body) {
      $json = $Body | ConvertTo-Json -Compress -Depth 10
      $request.Content = [System.Net.Http.StringContent]::new($json, [System.Text.Encoding]::UTF8, "application/json")
    }
    $response = $Session.Client.SendAsync($request).GetAwaiter().GetResult()
    try {
      $raw = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
      $parsed = $null
      if (-not [string]::IsNullOrWhiteSpace($raw)) {
        try { $parsed = $raw | ConvertFrom-Json } catch { $parsed = $null }
      }
      return [pscustomobject]@{
        Status = [int]$response.StatusCode
        Json = $parsed
        Raw = $raw
      }
    } finally {
      $response.Dispose()
    }
  } finally {
    $request.Dispose()
  }
}

function Assert-Status($Response, [int]$Expected, [string]$Step) {
  if ($Response.Status -ne $Expected) {
    throw "$Step returned HTTP $($Response.Status), expected $Expected. Body: $($Response.Raw)"
  }
}

function Start-CatalogFixture([int]$Port, [string]$SampleText) {
  return Start-Job -ScriptBlock {
    param([int]$ListenPort, [string]$Content)

    $sampleBytes = [System.Text.Encoding]::UTF8.GetBytes($Content)
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $ListenPort)
    $listener.Start()
    try {
      while ($true) {
        while (-not $listener.Pending()) {
          Start-Sleep -Milliseconds 50
        }
        $connection = $listener.AcceptTcpClient()
        try {
          $connection.ReceiveTimeout = 5000
          $connection.SendTimeout = 5000
          $stream = $connection.GetStream()
          $buffer = New-Object byte[] 16384
          $used = 0
          $headerEnd = -1
          while ($headerEnd -lt 0 -and $used -lt $buffer.Length) {
            $read = $stream.Read($buffer, $used, $buffer.Length - $used)
            if ($read -le 0) { break }
            $used += $read
            for ($index = [Math]::Max(0, $used - $read - 3); $index -le $used - 4; $index++) {
              if ($buffer[$index] -eq 13 -and $buffer[$index + 1] -eq 10 -and $buffer[$index + 2] -eq 13 -and $buffer[$index + 3] -eq 10) {
                $headerEnd = $index + 4
                break
              }
            }
          }

          $headerText = [System.Text.Encoding]::ASCII.GetString($buffer, 0, $used)
          $lines = $headerText -split "`r`n"
          $requestParts = $lines[0] -split " "
          $method = if ($requestParts.Count -ge 1) { $requestParts[0] } else { "" }
          $target = if ($requestParts.Count -ge 2) { $requestParts[1] } else { "/" }
          $path = ($target -split "\?", 2)[0]
          $headers = @{}
          foreach ($line in $lines | Select-Object -Skip 1) {
            $separator = $line.IndexOf(":")
            if ($separator -gt 0) {
              $headers[$line.Substring(0, $separator).Trim().ToLowerInvariant()] = $line.Substring($separator + 1).Trim()
            }
          }

          $status = "200 OK"
          $contentType = "application/json; charset=utf-8"
          $body = $null
          $hasDeviceAuth = $headers.ContainsKey("authorization") -and
            $headers["authorization"].StartsWith("Bearer ") -and
            $headers.ContainsKey("x-bosstransfer-device-id") -and
            -not [string]::IsNullOrWhiteSpace($headers["x-bosstransfer-device-id"])

          if ($method -eq "GET" -and $path -eq "/api/v1/health/live") {
            $body = [System.Text.Encoding]::UTF8.GetBytes('{"status":"ok"}')
          } elseif (($path -eq "/api/v1/catalog/search" -or $path -eq "/api/v1/catalog/resolve") -and -not $hasDeviceAuth) {
            $status = "401 Unauthorized"
            $body = [System.Text.Encoding]::UTF8.GetBytes('{"error":"device_token_invalid"}')
          } elseif ($method -eq "GET" -and $path -eq "/api/v1/catalog/search") {
            $payload = [ordered]@{
              items = @([ordered]@{
                resource_id = "smoke-resource"
                name = "demo-movie.txt"
                size = $sampleBytes.Length
                is_dir = $false
              })
            } | ConvertTo-Json -Compress -Depth 5
            $body = [System.Text.Encoding]::UTF8.GetBytes($payload)
          } elseif ($method -eq "POST" -and $path -eq "/api/v1/catalog/resolve") {
            $payload = [ordered]@{
              download = [ordered]@{
                url = "http://127.0.0.1:$ListenPort/sample/demo-movie.txt"
                name = "demo-movie.txt"
                size = $sampleBytes.Length
                is_dir = $false
                headers = @{}
              }
            } | ConvertTo-Json -Compress -Depth 5
            $body = [System.Text.Encoding]::UTF8.GetBytes($payload)
          } elseif ($method -eq "GET" -and $path -eq "/sample/demo-movie.txt") {
            $contentType = "text/plain; charset=utf-8"
            $body = $sampleBytes
          } else {
            $status = "404 Not Found"
            $body = [System.Text.Encoding]::UTF8.GetBytes('{"error":"not_found"}')
          }

          $responseHead = "HTTP/1.1 $status`r`nContent-Type: $contentType`r`nContent-Length: $($body.Length)`r`nConnection: close`r`nCache-Control: no-store`r`n`r`n"
          $responseBytes = [System.Text.Encoding]::ASCII.GetBytes($responseHead)
          $stream.Write($responseBytes, 0, $responseBytes.Length)
          $stream.Write($body, 0, $body.Length)
          $stream.Flush()
        } finally {
          $connection.Dispose()
        }
      }
    } finally {
      $listener.Stop()
    }
  } -ArgumentList $Port,$SampleText
}

function Stop-CatalogFixture($Job) {
  if ($Job) {
    Stop-Job -Job $Job -ErrorAction SilentlyContinue
    Remove-Job -Job $Job -Force -ErrorAction SilentlyContinue
  }
}

if (-not (Test-Path -LiteralPath "dist\manager.exe") -or -not (Test-Path -LiteralPath "dist\client.exe")) {
  throw "dist binaries are missing; run scripts\verify-phase1.ps1 first"
}

$sampleRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("bosstransfer-smoke-" + [guid]::NewGuid().ToString("N"))
$sourceDir = Join-Path $sampleRoot "source"
$targetDir = Join-Path $sampleRoot "target"
$managerDataDir = Join-Path $sampleRoot "manager-data"
$clientDataDir = Join-Path $sampleRoot "client-data"
New-Item -ItemType Directory -Force -Path $sourceDir,$targetDir,$managerDataDir,$clientDataDir | Out-Null

$managerPort = Get-FreePort
do { $clientPort = Get-FreePort } while ($clientPort -eq $managerPort)
$managerBase = "http://127.0.0.1:$managerPort"
$clientBase = "http://127.0.0.1:$clientPort"
$manager = $null
$client = $null
$catalogFixture = $null
$managerSession = $null
$clientSession = $null
$sampleText = "hello from the BossTransfer authenticated system download smoke test"
$managerUser = "manager-smoke"
$managerPassword = "manager-smoke-password"
$clientUser = "customer-smoke"
$clientPassword = "customer-smoke-password"

$managerEnvironment = @{
  BOSSTRANSFER_MANAGER_ADDR = "127.0.0.1:$managerPort"
  BOSSTRANSFER_MANAGER_PUBLIC_PORT = "$managerPort"
  BOSSTRANSFER_CLIENT_PUBLIC_PORT = "$clientPort"
  BOSSTRANSFER_DATA_DIR = $managerDataDir
  BOSSTRANSFER_MANAGER_USER = "admin"
  BOSSTRANSFER_MANAGER_PASSWORD = "password"
}
$clientEnvironment = @{
  BOSSTRANSFER_CLIENT_ADDR = "127.0.0.1:$clientPort"
  BOSSTRANSFER_MANAGER_URL = $managerBase
  BOSSTRANSFER_SOURCE_DIR = $sourceDir
  BOSSTRANSFER_TARGET_DIR = $targetDir
  BOSSTRANSFER_SOURCE_HOST_PATH = $sourceDir
  BOSSTRANSFER_TARGET_HOST_PATH = $targetDir
  BOSSTRANSFER_CLIENT_DATA_DIR = $clientDataDir
  BOSSTRANSFER_SETUP_USER = "admin"
  BOSSTRANSFER_SETUP_PASSWORD = "password"
}

try {
  $manager = Start-BossProcess "dist\manager.exe" $managerEnvironment
  $managerLive = Wait-JsonEndpoint "$managerBase/api/v1/health/live"
  $managerReady = Wait-JsonEndpoint "$managerBase/api/v1/health/ready"
  Wait-PageEndpoint "$managerBase/login" "BossTransfer" | Out-Null

  $managerSession = New-ApiSession $managerBase
  $unauthenticated = Invoke-JsonSession $managerSession "GET" "/api/v1/manager/licenses"
  Assert-Status $unauthenticated 401 "unauthenticated manager license list"

  $managerLogin = Invoke-JsonSession $managerSession "POST" "/api/v1/manager/auth/login" @{
    username = "admin"
    password = "password"
  } $managerBase
  Assert-Status $managerLogin 200 "manager default login"
  if (-not $managerLogin.Json.authenticated -or -not $managerLogin.Json.must_change) {
    throw "manager default account did not require its first password change"
  }
  if ($managerSession.Handler.CookieContainer.GetCookies([uri]$managerBase).Count -lt 1) {
    throw "manager login did not establish a session cookie"
  }

  $managerGate = Invoke-JsonSession $managerSession "POST" "/api/v1/manager/licenses" @{
    customer = "blocked-before-password-change"
    device_limit = 1
  } $managerBase
  Assert-Status $managerGate 428 "manager first-password gate"
  if ($managerGate.Json.error -ne "password_change_required") {
    throw "manager first-password gate returned an unexpected error"
  }

  $managerCrossSite = Invoke-JsonSession $managerSession "PUT" "/api/v1/manager/auth/credentials" @{
    current_password = "password"
    username = $managerUser
    password = $managerPassword
  } "https://attacker.invalid"
  Assert-Status $managerCrossSite 403 "manager cross-site credential change"
  if ($managerCrossSite.Json.error -ne "cross_site_request_blocked") {
    throw "manager CSRF origin guard returned an unexpected error"
  }

  $managerChange = Invoke-JsonSession $managerSession "PUT" "/api/v1/manager/auth/credentials" @{
    current_password = "password"
    username = $managerUser
    password = $managerPassword
  } $managerBase
  Assert-Status $managerChange 200 "manager credential change"
  $managerOldSession = Invoke-JsonSession $managerSession "GET" "/api/v1/manager/licenses"
  Assert-Status $managerOldSession 401 "manager session invalidation"
  $managerLogin = Invoke-JsonSession $managerSession "POST" "/api/v1/manager/auth/login" @{
    username = $managerUser
    password = $managerPassword
  } $managerBase
  Assert-Status $managerLogin 200 "manager changed-account login"
  if ($managerLogin.Json.must_change) {
    throw "manager changed account still requires a password change"
  }

  $created = Invoke-JsonSession $managerSession "POST" "/api/v1/manager/licenses" @{
    customer = "smoke-customer"
    device_limit = 1
  } $managerBase
  Assert-Status $created 201 "manager license creation"
  $licenseID = [string]$created.Json.license.id
  $authorizationCode = [string]$created.Json.code.authorization_code
  if ([string]::IsNullOrWhiteSpace($licenseID) -or [string]::IsNullOrWhiteSpace($authorizationCode)) {
    throw "manager license creation omitted the license ID or one-time authorization code"
  }
  $listed = Invoke-JsonSession $managerSession "GET" "/api/v1/manager/licenses"
  Assert-Status $listed 200 "manager license list"
  if ($listed.Raw.Contains($authorizationCode)) {
    throw "manager license list exposed the one-time authorization code"
  }

  $client = Start-BossProcess "dist\client.exe" $clientEnvironment
  $clientLive = Wait-JsonEndpoint "$clientBase/api/v1/health/live"
  $clientReady = Wait-JsonEndpoint "$clientBase/api/v1/health/ready"
  Wait-PageEndpoint "$clientBase/login" "BossTransfer" | Out-Null

  $clientSession = New-ApiSession $clientBase
  $clientLogin = Invoke-JsonSession $clientSession "POST" "/api/v1/client/auth/login" @{
    username = "admin"
    password = "password"
  } $clientBase
  Assert-Status $clientLogin 200 "client default login"
  if (-not $clientLogin.Json.authenticated -or -not $clientLogin.Json.must_change) {
    throw "client default account did not require its first password change"
  }
  if ($clientSession.Handler.CookieContainer.GetCookies([uri]$clientBase).Count -lt 1) {
    throw "client login did not establish a session cookie"
  }

  $clientGate = Invoke-JsonSession $clientSession "GET" "/api/v1/client/config"
  Assert-Status $clientGate 428 "client first-password gate"
  if ($clientGate.Json.error -ne "password_change_required") {
    throw "client first-password gate returned an unexpected error"
  }
  $clientCrossSite = Invoke-JsonSession $clientSession "PUT" "/api/v1/client/auth/credentials" @{
    current_password = "password"
    username = $clientUser
    password = $clientPassword
  } "https://attacker.invalid"
  Assert-Status $clientCrossSite 403 "client cross-site credential change"
  if ($clientCrossSite.Json.error -ne "cross_site_request_blocked") {
    throw "client CSRF origin guard returned an unexpected error"
  }
  $clientChange = Invoke-JsonSession $clientSession "PUT" "/api/v1/client/auth/credentials" @{
    current_password = "password"
    username = $clientUser
    password = $clientPassword
  } $clientBase
  Assert-Status $clientChange 200 "client credential change"
  $clientOldSession = Invoke-JsonSession $clientSession "GET" "/api/v1/client/license"
  Assert-Status $clientOldSession 401 "client session invalidation"
  $clientLogin = Invoke-JsonSession $clientSession "POST" "/api/v1/client/auth/login" @{
    username = $clientUser
    password = $clientPassword
  } $clientBase
  Assert-Status $clientLogin 200 "client changed-account login"
  if ($clientLogin.Json.must_change) {
    throw "client changed account still requires a password change"
  }

  $blockedSearch = Invoke-JsonSession $clientSession "GET" "/api/v1/client/search?q=demo"
  Assert-Status $blockedSearch 403 "unactivated client search"
  if ($blockedSearch.Json.error -ne "license_required") {
    throw "unactivated client search returned an unexpected error"
  }
  $activation = Invoke-JsonSession $clientSession "POST" "/api/v1/client/license/activate" @{
    manager_url = $managerBase
    code = $authorizationCode
    device_label = "smoke-client"
  } $clientBase
  Assert-Status $activation 200 "client online activation"
  if (-not $activation.Json.license.activated) {
    throw "client activation response was not active"
  }
  $downloaders = Invoke-JsonSession $clientSession "GET" "/api/v1/client/downloaders"
  Assert-Status $downloaders 200 "authorized downloader list"
  $systemDownloader = @($downloaders.Json.downloaders | Where-Object { $_.id -eq "system" })
  if ($systemDownloader.Count -ne 1 -or $systemDownloader[0].available -or -not ([string]$systemDownloader[0].reason).Contains("115")) {
    throw "system downloader did not require the customer's 115 binding"
  }

  $download = Invoke-JsonSession $clientSession "POST" "/api/v1/client/downloads" @{
    resource_id = "unresolved-before-115-binding"
    downloader = "system"
  } $clientBase
  Assert-Status $download 409 "customer 115 download gate"
  if ($download.Json.error -ne "account_115_required") {
    throw "download before customer 115 binding returned an unexpected error"
  }

  Stop-BossProcess $manager
  $manager = $null
  $managerEnvironment.BOSSTRANSFER_MANAGER_USER = "ignored-environment-user"
  $managerEnvironment.BOSSTRANSFER_MANAGER_PASSWORD = "ignored-environment-password"
  $manager = Start-BossProcess "dist\manager.exe" $managerEnvironment
  Wait-JsonEndpoint "$managerBase/api/v1/health/live" | Out-Null
  Remove-ApiSession $managerSession
  $managerSession = New-ApiSession $managerBase
  $managerLogin = Invoke-JsonSession $managerSession "POST" "/api/v1/manager/auth/login" @{
    username = $managerUser
    password = $managerPassword
  } $managerBase
  Assert-Status $managerLogin 200 "persisted manager Web account login"

  $revoked = Invoke-JsonSession $managerSession "POST" "/api/v1/manager/licenses/$licenseID/revoke" @{} $managerBase
  Assert-Status $revoked 200 "manager license revocation"

  Stop-BossProcess $client
  $client = $null
  Remove-ApiSession $clientSession
  $clientSession = $null
  $clientEnvironment.BOSSTRANSFER_SETUP_USER = "ignored-environment-user"
  $clientEnvironment.BOSSTRANSFER_SETUP_PASSWORD = "ignored-environment-password"
  $client = Start-BossProcess "dist\client.exe" $clientEnvironment
  Wait-JsonEndpoint "$clientBase/api/v1/health/live" | Out-Null
  $clientSession = New-ApiSession $clientBase
  $clientLogin = Invoke-JsonSession $clientSession "POST" "/api/v1/client/auth/login" @{
    username = $clientUser
    password = $clientPassword
  } $clientBase
  Assert-Status $clientLogin 200 "persisted client Web account login"

  $revocationDeadline = [DateTime]::UtcNow.AddSeconds(15)
  $licenseStatus = $null
  do {
    $licenseResponse = Invoke-JsonSession $clientSession "GET" "/api/v1/client/license"
    Assert-Status $licenseResponse 200 "revoked client license status"
    $licenseStatus = $licenseResponse.Json.license
    if (-not $licenseStatus.activated -and $licenseStatus.license.status -eq "revoked") { break }
    Start-Sleep -Milliseconds 200
  } while ([DateTime]::UtcNow -lt $revocationDeadline)
  if ($licenseStatus.activated -or $licenseStatus.license.status -ne "revoked") {
    throw "client did not apply manager revocation after restart heartbeat"
  }
  $revokedSearch = Invoke-JsonSession $clientSession "GET" "/api/v1/client/search?q=demo"
  Assert-Status $revokedSearch 403 "revoked client search"
  if ($revokedSearch.Json.error -ne "license_required") {
    throw "revoked client search returned an unexpected error"
  }

  [pscustomobject]@{
    ok = $true
    manager_live = $managerLive.status
    manager_ready = $managerReady.status
    client_live = $clientLive.status
    client_ready = $clientReady.status
    cookie_sessions = "passed"
    csrf_origin_guards = "passed"
    forced_password_change = "passed"
    persistent_web_accounts = "passed"
    license_activation = "passed"
    customer_115_download_gate = "passed"
    revocation_gate = "passed"
  } | ConvertTo-Json -Depth 4
} finally {
  Remove-ApiSession $clientSession
  Remove-ApiSession $managerSession
  Stop-CatalogFixture $catalogFixture
  Stop-BossProcess $client
  Stop-BossProcess $manager
  if (Test-Path -LiteralPath $sampleRoot) {
    Remove-Item -LiteralPath $sampleRoot -Recurse -Force
  }
}
