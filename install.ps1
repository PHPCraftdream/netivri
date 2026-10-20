# netivri standalone installer for Windows (PowerShell 5.1 and 7).
#
# Downloads the release zip, verifies its SHA256 checksum against the
# published SHA256SUMS, and installs netivri.exe plus the Ktav native
# library (ktav_cabi.dll, which must sit next to the binary).
# See docs/install.md.

param(
  [string]$Version,
  [string]$InstallDir,
  [string]$BaseUrl,
  [string]$Proxy,
  [switch]$AddToPath,
  [switch]$Uninstall,
  [switch]$Help
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072

$Repo = 'PHPCraftdream/netivri'
$LibName = 'ktav_cabi.dll'

function Fail([string]$Message) {
  [Console]::Error.WriteLine("install.ps1: error: $Message")
  exit 1
}

function Show-Usage {
  Write-Host "Usage: install.ps1 [options]

Options:
  -Version X.Y.Z   Install this version (default: latest release).
                   Env: NETIVRI_VERSION
  -InstallDir DIR  Install directory (default: %LOCALAPPDATA%\netivri\bin).
                   Env: NETIVRI_INSTALL_DIR
  -BaseUrl URL     Download base URL: https://, http://, file:// or a
                   plain local directory holding the zips and SHA256SUMS.
                   Env: NETIVRI_BASE_URL
  -Proxy URL       HTTP(S) proxy for Invoke-WebRequest. Without it the
                   system proxy settings are used.
  -AddToPath       Add the install dir to the USER PATH.
  -Uninstall       Remove the installed files.
  -Help            Show this help.

Exit codes: 0 success, 1 failure, 2 usage error."
}

if ($Help) { Show-Usage; exit 0 }

if (-not $Version) { $Version = $env:NETIVRI_VERSION }
if (-not $Version) { $Version = '' }
if (-not $InstallDir) { $InstallDir = $env:NETIVRI_INSTALL_DIR }
if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA 'netivri\bin' }
if (-not $BaseUrl) { $BaseUrl = $env:NETIVRI_BASE_URL }
if (-not $BaseUrl) { $BaseUrl = '' }
# The version ends up in URLs and file names: allow only version characters.
if ($Version -and $Version -notmatch '^[0-9A-Za-z.+-]+$') { Fail "invalid version: $Version" }

$procArch = $env:PROCESSOR_ARCHITEW6432
if (-not $procArch) { $procArch = $env:PROCESSOR_ARCHITECTURE }
switch ($procArch) {
  'AMD64' { $GoArch = 'amd64' }
  'ARM64' { $GoArch = 'arm64' }
  default { Fail "unsupported architecture: $procArch (supported: AMD64, ARM64)" }
}

# Copy-LocalFile SRC DST: copy a file:// URL or a plain local path.
function Copy-LocalFile([string]$Src, [string]$Dst) {
  $path = $Src
  if ($Src -like 'file://*') {
    $path = ([System.Uri]$Src).LocalPath
  }
  if (-not (Test-Path -LiteralPath $path)) {
    Fail "file not found: $path"
  }
  Copy-Item -LiteralPath $path -Destination $Dst -Force
}

# Get-RemoteFile URL DST: download or copy URL to DST. HTTP(S) goes
# through the system proxy settings (or -Proxy when given).
function Get-RemoteFile([string]$Url, [string]$Dst) {
  if ($Url -like 'file://*' -or $Url -notmatch '^[a-zA-Z][a-zA-Z0-9+.-]*://') {
    Copy-LocalFile $Url $Dst
    return
  }
  $req = @{
    Uri = $Url
    OutFile = $Dst
    UseBasicParsing = $true
  }
  if ($Proxy) { $req['Proxy'] = $Proxy }
  Invoke-WebRequest @req
}

# Get-LatestVersion: resolve the latest release tag via the
# github.com/.../releases/latest redirect (no API call).
function Get-LatestVersion {
  $url = "https://github.com/$Repo/releases/latest"
  $webReq = [System.Net.HttpWebRequest]::Create($url)
  $webReq.Method = 'HEAD'
  $webReq.AllowAutoRedirect = $false
  $webReq.Timeout = 30000
  if ($Proxy) { $webReq.Proxy = New-Object System.Net.WebProxy($Proxy) }
  $resp = $webReq.GetResponse()
  try {
    $loc = $resp.Headers['Location']
  } finally {
    $resp.Close()
  }
  if (-not $loc) { Fail "cannot resolve the latest release tag from $url" }
  $tag = $loc.TrimEnd('/').Split('/')[-1]
  if (-not $tag.StartsWith('v')) { Fail "unexpected redirect when resolving the latest release: $loc" }
  return $tag.Substring(1)
}

# Install-File SRC DST: copy via a temp name, then rename.
function Install-File([string]$Src, [string]$Dst) {
  $tmpDst = "$Dst.tmp"
  Copy-Item -LiteralPath $Src -Destination $tmpDst -Force
  Move-Item -LiteralPath $tmpDst -Destination $Dst -Force
}

function Invoke-Uninstall {
  $share = Join-Path $InstallDir '..\share\netivri'
  $removed = $false
  foreach ($f in @((Join-Path $InstallDir 'netivri.exe'), (Join-Path $InstallDir $LibName))) {
    if (Test-Path -LiteralPath $f) {
      Remove-Item -LiteralPath $f -Force
      Write-Host "removed $f"
      $removed = $true
    }
  }
  if (Test-Path -LiteralPath $share) {
    Remove-Item -LiteralPath $share -Recurse -Force
    Write-Host "removed $share"
    $removed = $true
  }
  if (-not $removed) { Write-Host "nothing to uninstall in $InstallDir" }
}

if ($Uninstall) {
  Invoke-Uninstall
  exit 0
}

if (-not $Version) {
  if ($BaseUrl) {
    Fail "when -BaseUrl is used, -Version (or NETIVRI_VERSION) must also be given"
  }
  $Version = Get-LatestVersion
  Write-Host "latest release: $Version"
}

$Archive = "netivri_${Version}_windows_${GoArch}.zip"

if ($BaseUrl) {
  $base = $BaseUrl.TrimEnd('/')
  $archiveUrl = "$base/$Archive"
  $sumsUrl = "$base/SHA256SUMS"
} else {
  $dl = "https://github.com/$Repo/releases/download/v$Version"
  $archiveUrl = "$dl/$Archive"
  $sumsUrl = "$dl/SHA256SUMS"
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("netivri-install-" + [System.Guid]::NewGuid().ToString('N'))
try {
  New-Item -ItemType Directory -Path $tmp -Force | Out-Null
  $zipPath = Join-Path $tmp $Archive
  $sumsPath = Join-Path $tmp 'SHA256SUMS'

  Write-Host "downloading $archiveUrl"
  Get-RemoteFile $archiveUrl $zipPath
  Get-RemoteFile $sumsUrl $sumsPath

  # Verify the checksum BEFORE anything is extracted or installed.
  $expected = $null
  foreach ($line in (Get-Content -LiteralPath $sumsPath)) {
    $parts = $line -split '\s+'
    if ($parts.Count -ge 2 -and $parts[0] -match '^[0-9a-fA-F]{64}$' -and $parts[1].TrimStart('*') -eq $Archive) {
      $expected = $parts[0]
      break
    }
  }
  if (-not $expected) {
    Fail "SHA256SUMS has no entry for $Archive"
  }
  $actual = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash
  if (-not [string]::Equals($actual, $expected, [StringComparison]::OrdinalIgnoreCase)) {
    Fail "checksum mismatch for ${Archive}: expected $expected, got $actual"
  }

  $exDir = Join-Path $tmp 'extracted'
  New-Item -ItemType Directory -Path $exDir -Force | Out-Null
  Expand-Archive -Path $zipPath -DestinationPath $exDir

  $src = Join-Path $exDir "netivri_${Version}_windows_${GoArch}"
  if (-not (Test-Path -LiteralPath $src)) {
    $cand = Get-ChildItem -Path $exDir -Directory | Select-Object -First 1
    if ($cand) { $src = $cand.FullName } else { Fail 'the archive is empty' }
  }
  if (-not (Test-Path -LiteralPath (Join-Path $src 'netivri.exe'))) {
    Fail 'the archive does not contain netivri.exe'
  }
  if (-not (Test-Path -LiteralPath (Join-Path $src $LibName))) {
    Fail "the archive does not contain $LibName"
  }

  $share = Join-Path $InstallDir '..\share\netivri'
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  New-Item -ItemType Directory -Path (Join-Path $share 'THIRD_PARTY_LICENSES') -Force | Out-Null

  Install-File (Join-Path $src 'netivri.exe') (Join-Path $InstallDir 'netivri.exe')
  Install-File (Join-Path $src $LibName) (Join-Path $InstallDir $LibName)
  Copy-Item -LiteralPath (Join-Path $src 'LICENSE') -Destination (Join-Path $share 'LICENSE') -Force
  Copy-Item -Path (Join-Path $src 'THIRD_PARTY_LICENSES\*') -Destination (Join-Path $share 'THIRD_PARTY_LICENSES') -Recurse -Force

  Write-Host "installed netivri $Version to $InstallDir"

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $onPath = @($userPath -split ';') -contains $InstallDir
  if ($AddToPath) {
    if (-not $onPath) {
      if ($userPath) { $newPath = "$userPath;$InstallDir" } else { $newPath = $InstallDir }
      [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
      Write-Host "added $InstallDir to the user PATH (takes effect in new terminals)"
    }
  } elseif (-not $onPath) {
    Write-Host "$InstallDir is not on your PATH. Re-run with -AddToPath to add it to the user PATH."
  }
} finally {
  if (Test-Path -LiteralPath $tmp) {
    Remove-Item -LiteralPath $tmp -Recurse -Force
  }
}
