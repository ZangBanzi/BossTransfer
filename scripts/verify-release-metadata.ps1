$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Assert-Contains([string]$Path, [string]$Value, [string]$Message) {
  $text = Get-Content -LiteralPath $Path -Raw
  if (-not $text.Contains($Value)) {
    throw "$Message ($Path)"
  }
}

$version = (Get-Content -LiteralPath "VERSION" -Raw).Trim()
if ($version -notmatch '^[0-9]+[.][0-9]+[.][0-9]+$') {
  throw "VERSION is not a release semantic version: $version"
}

Assert-Contains "internal/buildinfo/buildinfo.go" ('Version = "' + $version + '"') "buildinfo version does not match VERSION"
Assert-Contains "api/openapi.yaml" ("  version: $version") "OpenAPI version does not match VERSION"
Assert-Contains "web/package.json" ('"version": "' + $version + '"') "Web package version does not match VERSION"
Assert-Contains "README.md" ("BossTransfer $version") "README release version is missing"
Assert-Contains "CHANGELOG.md" ("## $version") "CHANGELOG release entry is missing"

$composeExpected = @{
  "deploy/docker/docker-compose.yml" = @("bosstransfer/manager:$version", "bosstransfer/client:$version")
  "deploy/docker/docker-compose.manager.yml" = @("bosstransfer/manager:$version")
  "deploy/docker/docker-compose.client.yml" = @("bosstransfer/client:$version")
}
foreach ($entry in $composeExpected.GetEnumerator()) {
  $text = Get-Content -LiteralPath $entry.Key -Raw
  foreach ($image in $entry.Value) {
    if (-not $text.Contains("image: $image")) {
      throw "Compose image tag does not match VERSION: $($entry.Key) -> $image"
    }
  }
}

$envText = Get-Content -LiteralPath "deploy/docker/.env.example" -Raw
foreach ($value in @(
  "BOSSTRANSFER_SETUP_USER=admin",
  "BOSSTRANSFER_SETUP_PASSWORD=password",
  "BOSSTRANSFER_MANAGER_USER=admin",
  "BOSSTRANSFER_MANAGER_PASSWORD=password",
  "BOSSTRANSFER_MANAGER_DATA_VOLUME=bosstransfer_manager-data",
  "BOSSTRANSFER_CLIENT_DATA_VOLUME=bosstransfer_client-data"
)) {
  if (-not $envText.Contains($value)) {
    throw "Missing documented environment default: $value"
  }
}
if ([regex]::Matches($envText, '(?i)only if no saved account exists').Count -lt 2) {
  throw ".env.example must state that Web credentials are first-start bootstrap values"
}

$openapi = Get-Content -LiteralPath "api/openapi.yaml" -Raw
if ($openapi.Contains([char]9)) {
  throw "OpenAPI YAML contains a tab"
}
foreach ($header in @("openapi: 3.1.0", "paths:", "components:", "securitySchemes:", "schemas:")) {
  if (-not $openapi.Contains($header)) {
    throw "OpenAPI YAML is missing: $header"
  }
}

$routeSources = @("apps/manager/main.go", "apps/client/main.go")
$routes = New-Object System.Collections.Generic.HashSet[string]
foreach ($source in $routeSources) {
  $text = Get-Content -LiteralPath $source -Raw
  foreach ($match in [regex]::Matches($text, '"/api/v1/[^"]+"')) {
    $route = $match.Value.Trim('"').Substring(7).TrimEnd('/')
    if ($route -notmatch '/health/(live|ready)$') {
      [void]$routes.Add($route)
    }
  }
}
$documentedPaths = New-Object System.Collections.Generic.HashSet[string]
foreach ($match in [regex]::Matches($openapi, '(?m)^  (/[^:]+):[ ]*$')) {
  [void]$documentedPaths.Add($match.Groups[1].Value.TrimEnd('/'))
}
$missingRoutes = @($routes | Where-Object {
  $registered = $_
  -not @($documentedPaths | Where-Object { $_ -eq $registered -or $_.StartsWith($registered + "/") }).Count
})
if ($missingRoutes.Count -gt 0) {
  throw "OpenAPI is missing registered route roots: $($missingRoutes -join ', ')"
}

$requiredDocs = @(
  "docs/architecture.md",
  "docs/deployment-production.md",
  "docs/customer-operation-guide.md",
  "docs/github-upload-guide.md",
  "docs/security-model.md",
  "docs/legacy-migration.md"
)
foreach ($doc in $requiredDocs) {
  if (-not (Test-Path -LiteralPath $doc)) {
    throw "Missing release document: $doc"
  }
}
foreach ($term in @("admin", "password", "CloudDrive2", "qBittorrent", "授权")) {
  if (-not (Get-Content -LiteralPath "README.md" -Raw).Contains($term)) {
    throw "README is missing current architecture term: $term"
  }
}

$previousErrorActionPreference = $ErrorActionPreference
$ErrorActionPreference = "Continue"
$diffCheck = @(git diff --check 2>&1)
$diffExitCode = $LASTEXITCODE
$ErrorActionPreference = $previousErrorActionPreference
if ($diffExitCode -ne 0) {
  throw "git diff --check failed: $($diffCheck -join '; ')"
}

[pscustomobject]@{
  ok = $true
  version = $version
  compose_files = $composeExpected.Count
  documented_route_roots = $documentedPaths.Count
  registered_route_roots = $routes.Count
  release_documents = $requiredDocs.Count
  git_diff_check = "passed"
} | ConvertTo-Json -Depth 3
