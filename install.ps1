# terva installer for Windows (PowerShell) — downloads from the
# project's release host (see the channel-identity block below; never
# from upstream).
#
# Usage (in PowerShell):
#   iwr -useb https://terva.sh/install.ps1 | iex
#
# Or with arguments:
#   $env:TERVA_VERSION = "v0.0.1"
#   $env:TERVA_PREFIX  = "$HOME\bin"
#   iwr -useb https://terva.sh/install.ps1 | iex
#
# Detects architecture, downloads the matching .zip from the release,
# verifies the sha256 against checksums.txt, extracts terva.exe,
# and moves it into $TERVA_PREFIX (defaults to $HOME\bin, added to PATH
# via the User environment if missing).
#
# An access token (the env var named by $tokenVar below) is optional
# for a public repo. Set it to a token with read access if the repo is
# private; the script then uses it for the version lookup and every
# download.


[CmdletBinding()]
param(
  [string]$Version = $env:TERVA_VERSION,
  [string]$Prefix  = $env:TERVA_PREFIX
)

$ErrorActionPreference = "Stop"

# --- channel identity (the release-cut rewrites this block; scripts/release.sh) ---
$gitHost   = "https://github.com"
$owner     = "terva-sh"
$repo      = "terva"
$apiLatest = "https://api.github.com/repos/$owner/$repo/releases/latest"
$tokenVar  = "GITHUB_TOKEN"
# --- end channel identity ---

$binary = "terva"

# Build Authorization header list once; used on every HTTP call so the
# script works against private repos when the channel's token variable
# is set.
$token = [Environment]::GetEnvironmentVariable($tokenVar)
$headers = @{}
if ($token) { $headers["Authorization"] = "Bearer $token" }

if (-not $Version) { $Version = "latest" }
if (-not $Prefix)  { $Prefix  = Join-Path $HOME "bin" }

function Msg($m)  { Write-Host "==> $m" -ForegroundColor Cyan }
function Warn($m) { Write-Warning $m }
function Die($m)  { Write-Error $m; exit 1 }

# ---- detect architecture ----

switch -wildcard ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { $arch = "amd64" }
  "ARM64" { $arch = "arm64" }
  default { Die "unsupported arch: $($env:PROCESSOR_ARCHITECTURE)" }
}

# ARM64 Windows isn't shipped (see .goreleaser.yaml ignore rule) — fall
# back to amd64 which runs fine under ARM64 emulation.
if ($arch -eq "arm64") {
  Warn "windows/arm64 is not published; falling back to amd64"
  $arch = "amd64"
}

# ---- resolve version ----
#
# Resolve "latest" through the Forgejo releases API (GitHub-compatible
# tag_name field). This works the same on Windows PowerShell 5.1 and
# PowerShell 7+, unlike scraping the /releases/latest redirect target:
# on PS7 the final URL lives at
# $resp.BaseResponse.RequestMessage.RequestUri while on PS5.1 it is
# $resp.BaseResponse.ResponseUri, and relying on either breaks on the
# other runtime. The API returns the tag directly, so there is nothing
# to scrape.

if ($Version -eq "latest") {
  $apiUrl = $apiLatest
  # Be explicit about User-Agent so corporate proxies that strip the
  # default don't trip a 403.
  $apiHeaders = @{} + $headers
  if (-not $apiHeaders.ContainsKey("User-Agent")) { $apiHeaders["User-Agent"] = "terva-installer" }
  $apiHeaders["Accept"] = "application/json"

  try {
    $api = Invoke-RestMethod -UseBasicParsing -Headers $apiHeaders -Uri $apiUrl
  } catch {
    $status = $null
    try { $status = [int]$_.Exception.Response.StatusCode } catch {}
    if ($status -eq 404) {
      Die "no published release found for $owner/$repo (the repo may have no releases yet)"
    } elseif ($status -eq 401 -or $status -eq 403) {
      Die "release API request was rejected ($status). If the repo is private, set `$env:$tokenVar to an access token with read access."
    } else {
      Die "could not resolve latest version: $($_.Exception.Message)"
    }
  }

  $Version = $api.tag_name
  if (-not $Version) {
    Die "could not resolve latest version: release API returned no tag_name for $owner/$repo"
  }
}

if (-not $Version.StartsWith("v")) { $Version = "v$Version" }
$verNum = $Version.TrimStart("v")

# ---- download + verify + extract ----

$archive     = "${binary}_${verNum}_windows_${arch}.zip"
$baseUrl     = "$gitHost/$owner/$repo/releases/download/$Version"
$archiveUrl  = "$baseUrl/$archive"
$checksumUrl = "$baseUrl/checksums.txt"

$tmp = New-Item -ItemType Directory -Path (Join-Path $env:TEMP ("terva-install-" + [System.Guid]::NewGuid().ToString("N").Substring(0,8)))

try {
  Msg "downloading $archive"
  Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri $archiveUrl -OutFile (Join-Path $tmp $archive)

  Msg "verifying checksum"
  $checksumFile = Join-Path $tmp "checksums.txt"
  Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri $checksumUrl -OutFile $checksumFile
  $expected = Get-Content -LiteralPath $checksumFile | ForEach-Object {
    $line = $_.Trim()
    if ($line) {
      $parts = $line -split "\s+"
      if ($parts.Count -ge 2 -and $parts[($parts.Count - 1)] -eq $archive) { $line }
    }
  } | Select-Object -First 1
  if (-not $expected) { Die "no checksum for $archive in checksums.txt" }
  $expectedHash = ($expected -split "\s+")[0]

  $actualHash = (Get-FileHash -Path (Join-Path $tmp $archive) -Algorithm SHA256).Hash.ToLower()
  if ($expectedHash.ToLower() -ne $actualHash) {
    Die "checksum mismatch: expected $expectedHash, got $actualHash"
  }

  Msg "extracting"
  Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp -Force

  $exe = Join-Path $tmp "$binary.exe"
  if (-not (Test-Path $exe)) { Die "archive did not contain $binary.exe" }

  Msg "installing to $Prefix\$binary.exe"
  New-Item -ItemType Directory -Path $Prefix -Force | Out-Null
  Copy-Item $exe (Join-Path $Prefix "$binary.exe") -Force

  # rename compat: terva was formerly zot; keep old scripts working # rename:keep
  # for a release cycle with a copy (windows has no easy symlinks). # rename:keep
  Copy-Item $exe (Join-Path $Prefix "zot.exe") -Force # rename:keep

  # ---- PATH hint ----

  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
  $parts = $userPath -split ";" | Where-Object { $_ }
  if (-not ($parts -contains $Prefix)) {
    Warn "$Prefix is not on your user PATH"
    Warn "adding it for future sessions..."
    [Environment]::SetEnvironmentVariable("Path", ($userPath.TrimEnd(";") + ";" + $Prefix), "User")
    Warn "open a new terminal to pick up the change, or run:"
    Warn "  `$env:Path = `"$Prefix;`$env:Path`""
  }

  Msg "installed. run:  terva --help"
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
