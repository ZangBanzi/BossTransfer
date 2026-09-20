function Assert-CleanReleaseTag([string]$Version) {
  $expectedTag = "v$Version"
  $status = @(git status --porcelain)
  if ($LASTEXITCODE -ne 0) {
    throw "Unable to inspect the Git worktree."
  }
  if ($status.Count -ne 0) {
    throw "Release builds require a clean Git worktree. Commit all changes first."
  }

  $exactTag = (git describe --exact-match --tags HEAD 2>$null)
  if ($LASTEXITCODE -ne 0 -or $exactTag.Trim() -ne $expectedTag) {
    throw "Release builds require HEAD to have the exact tag $expectedTag."
  }
}

function Find-CABundle {
  $candidates = [System.Collections.Generic.List[string]]::new()
  if (-not [string]::IsNullOrWhiteSpace($env:SSL_CERT_FILE)) {
    $candidates.Add($env:SSL_CERT_FILE)
  }

  $gitCommand = Get-Command git -ErrorAction SilentlyContinue
  if ($gitCommand) {
    $gitRoot = Split-Path -Parent (Split-Path -Parent $gitCommand.Source)
    $candidates.Add((Join-Path $gitRoot "mingw64\etc\ssl\certs\ca-bundle.crt"))
    $candidates.Add((Join-Path $gitRoot "usr\ssl\certs\ca-bundle.crt"))
  }

  @(
    "C:\Program Files\Git\mingw64\etc\ssl\certs\ca-bundle.crt",
    "C:\Program Files\Git\usr\ssl\certs\ca-bundle.crt",
    "/etc/ssl/certs/ca-certificates.crt",
    "/etc/pki/tls/certs/ca-bundle.crt",
    "/etc/ssl/ca-bundle.pem"
  ) | ForEach-Object { $candidates.Add($_) }

  foreach ($candidate in $candidates) {
    if ([string]::IsNullOrWhiteSpace($candidate) -or -not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
      continue
    }
    if (Select-String -LiteralPath $candidate -SimpleMatch "-----BEGIN CERTIFICATE-----" -Quiet) {
      return (Resolve-Path -LiteralPath $candidate).Path
    }
  }
  throw "A PEM CA bundle was not found. Set SSL_CERT_FILE to build an offline Docker package."
}

function Write-OfflineRuntimeFiles([string]$Stage) {
  $caBundle = Find-CABundle
  Copy-Item -LiteralPath $caBundle -Destination (Join-Path $Stage "ca-certificates.crt") -Force
  $releaseVersion = (Get-Content -LiteralPath "VERSION" -Raw).Trim()

  $managerDockerfile = @"
FROM scratch
LABEL org.opencontainers.image.title="BossTransfer Manager" \
      org.opencontainers.image.version="$releaseVersion" \
      org.opencontainers.image.description="BossTransfer central 115 catalog and authorization manager"
COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 bin/manager /manager
USER 65532:65532
EXPOSE 8085
ENTRYPOINT ["/manager"]
"@
  $clientDockerfile = @"
FROM scratch
LABEL org.opencontainers.image.title="BossTransfer Client" \
      org.opencontainers.image.version="$releaseVersion" \
      org.opencontainers.image.description="BossTransfer customer NAS 115 download client"
COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 bin/client /client
USER 65532:65532
EXPOSE 18085
ENTRYPOINT ["/client"]
"@
  [System.IO.File]::WriteAllText((Join-Path $Stage "manager-runtime.Dockerfile"), $managerDockerfile, [System.Text.UTF8Encoding]::new($false))
  [System.IO.File]::WriteAllText((Join-Path $Stage "client-runtime.Dockerfile"), $clientDockerfile, [System.Text.UTF8Encoding]::new($false))
}
