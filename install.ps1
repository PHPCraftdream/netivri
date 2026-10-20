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
  [string]$FromDir,
  [string]$Proxy,
  [switch]$AddToPath,
  [switch]$Quiet,
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

function UsageFail([string]$Message) {
  [Console]::Error.WriteLine("install.ps1: error: $Message")
  exit 2
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
  -FromDir DIR     Install from a local directory (archive layout or npm
                   platform package layout) instead of downloading. No
                   checksum is verified. Env: NETIVRI_FROM_DIR
  -Proxy URL       HTTP(S) proxy for Invoke-WebRequest. Without it the
                   system proxy settings are used.
  -AddToPath       Add the install dir to the USER PATH.
  -Quiet           Print nothing informational (progress, results, notes,
                   PATH hints); errors still go to stderr.
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
if (-not $FromDir) { $FromDir = $env:NETIVRI_FROM_DIR }
if (-not $FromDir) { $FromDir = '' }
# The version ends up in URLs and file names: allow only version characters.
if ($Version -and $Version -notmatch '^[0-9A-Za-z.+-]+$') { Fail "invalid version: $Version" }

if ($FromDir) {
  if ($Version -or $BaseUrl) {
    UsageFail "-FromDir cannot be combined with -Version or -BaseUrl (or their env variables)"
  }
}

# Write-Info MSG: informational output, suppressed by -Quiet.
function Write-Info([string]$Message) {
  if (-not $Quiet) { Write-Host $Message }
}

# No download progress bar in -Quiet mode (it is also much faster).
if ($Quiet) { $ProgressPreference = 'SilentlyContinue' }

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

# Get-RunningNetivriPid: the pid of a netivri process running the
# installed exe, or $null. Best effort: other users' processes throw on
# .Path access and are skipped.
function Get-RunningNetivriPid {
  try {
    $procs = Get-Process -Name netivri -ErrorAction Stop
  } catch {
    return $null
  }
  $installedExe = [System.IO.Path]::GetFullPath((Join-Path $InstallDir 'netivri.exe'))
  foreach ($p in $procs) {
    try {
      $path = $p.Path
    } catch {
      continue
    }
    if ($path -and ([System.IO.Path]::GetFullPath($path) -ieq $installedExe)) {
      return $p.Id
    }
  }
  return $null
}

# Remove-OldFiles: delete the *.old-* leftovers of netivri.exe and
# ktav_cabi.dll from earlier installs. Locked files stay and are counted.
function Remove-OldFiles {
  $locked = 0
  foreach ($name in @('netivri.exe', $LibName)) {
    $old = Get-ChildItem -LiteralPath $InstallDir -Filter "$name.old-*" -File -ErrorAction SilentlyContinue
    foreach ($f in $old) {
      try {
        Remove-Item -LiteralPath $f.FullName -Force -ErrorAction Stop
      } catch {
        $locked++
      }
    }
  }
  if ($locked -gt 0) {
    Write-Info "note: $locked old file(s) are still in use and will be removed by the next install or uninstall"
  }
}

# Install-File SRC DST: copy SRC next to DST under a temp name, then move
# it into place. A running netivri.exe or a mapped ktav_cabi.dll cannot
# be overwritten or deleted, but it CAN be renamed: so an existing DST is
# always renamed aside first, and the fresh copy takes its place. The old
# file is deleted at once when nothing holds it open. On failure the old
# file is renamed back, so the install dir never loses its binary.
function Install-File([string]$Src, [string]$Dst) {
  $dir = Split-Path -Parent $Dst
  $name = Split-Path -Leaf $Dst
  $newName = "$name.new-" + [System.Guid]::NewGuid().ToString('N').Substring(0, 8)
  $newPath = Join-Path $dir $newName
  Copy-Item -LiteralPath $Src -Destination $newPath -Force
  if (-not (Test-Path -LiteralPath $Dst)) {
    Move-Item -LiteralPath $newPath -Destination $Dst -Force
    return
  }
  $oldName = "$name.old-" + (Get-Date).ToString('yyyyMMddHHmmssfff')
  $oldPath = Join-Path $dir $oldName
  try {
    Rename-Item -LiteralPath $Dst -NewName $oldName
  } catch {
    Remove-Item -LiteralPath $newPath -Force
    Fail "cannot replace $Dst (rename aside failed): $($_.Exception.Message)"
  }
  try {
    Move-Item -LiteralPath $newPath -Destination $Dst -Force
  } catch {
    Rename-Item -LiteralPath $oldPath -NewName $name
    Remove-Item -LiteralPath $newPath -Force
    Fail "cannot install $Dst (moving the new file into place failed)"
  }
  try {
    Remove-Item -LiteralPath $oldPath -Force -ErrorAction Stop
  } catch {
    # Still in use: Remove-OldFiles collects it later.
  }
}

# Invoke-Install SRC VER LICROOT: shared install steps for both source
# modes. SRC holds the binary and library; VER is the version string, empty
# when unknown (-FromDir); LICROOT holds LICENSE and THIRD_PARTY_LICENSES
# (not SRC for an npm package, where SRC is DIR\bin).
function Invoke-Install([string]$Src, [string]$Ver, [string]$LicRoot) {
  # Looked up BEFORE the replacement: once the running exe is renamed
  # aside, its process path no longer equals the installed path.
  $runningPid = Get-RunningNetivriPid

  $share = Join-Path $InstallDir '..\share\netivri'
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  New-Item -ItemType Directory -Path (Join-Path $share 'THIRD_PARTY_LICENSES') -Force | Out-Null

  Install-File (Join-Path $Src 'netivri.exe') (Join-Path $InstallDir 'netivri.exe')
  Install-File (Join-Path $Src $LibName) (Join-Path $InstallDir $LibName)

  $license = Join-Path $LicRoot 'LICENSE'
  $third = Join-Path $LicRoot 'THIRD_PARTY_LICENSES'
  if ($FromDir) {
    # Optional in -FromDir mode; npm's integrity check covers the npm path.
    if (Test-Path -LiteralPath $license) {
      Copy-Item -LiteralPath $license -Destination (Join-Path $share 'LICENSE') -Force
    }
    if (Test-Path -LiteralPath $third) {
      Copy-Item -Path (Join-Path $third '*') -Destination (Join-Path $share 'THIRD_PARTY_LICENSES') -Recurse -Force
    }
  } else {
    Copy-Item -LiteralPath $license -Destination (Join-Path $share 'LICENSE') -Force
    Copy-Item -Path (Join-Path $third '*') -Destination (Join-Path $share 'THIRD_PARTY_LICENSES') -Recurse -Force
  }

  if ($runningPid) {
    Write-Info "note: netivri is running (pid $runningPid); it keeps using the previous version until restarted: netivri down && netivri up"
  }
  # Collects (and counts) the files the running process still holds.
  Remove-OldFiles

  if ($Ver) {
    Write-Info "installed netivri $Ver to $InstallDir"
  } else {
    Write-Info "installed netivri to $InstallDir"
  }

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $onPath = @($userPath -split ';') -contains $InstallDir
  if ($AddToPath) {
    if (-not $onPath) {
      if ($userPath) { $newPath = "$userPath;$InstallDir" } else { $newPath = $InstallDir }
      [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
      Write-Info "added $InstallDir to the user PATH (takes effect in new terminals)"
    }
  } elseif (-not $onPath) {
    Write-Info "$InstallDir is not on your PATH. Re-run with -AddToPath to add it to the user PATH."
  }
}

function Invoke-Uninstall {
  $share = Join-Path $InstallDir '..\share\netivri'
  $existed = $false
  foreach ($name in @('netivri.exe', $LibName)) {
    $left = Get-ChildItem -LiteralPath $InstallDir -Filter "$name.old-*" -File -ErrorAction SilentlyContinue
    if ($left) { $existed = $true }
  }
  foreach ($f in @((Join-Path $InstallDir 'netivri.exe'), (Join-Path $InstallDir $LibName))) {
    if (Test-Path -LiteralPath $f) {
      $existed = $true
      try {
        Remove-Item -LiteralPath $f -Force -ErrorAction Stop
        Write-Info "removed $f"
      } catch {
        # A running process keeps the file open: rename it aside (a
        # rename works even when delete does not) and let
        # Remove-OldFiles try again.
        $aside = "$(Split-Path -Leaf $f).old-" + (Get-Date).ToString('yyyyMMddHHmmssfff')
        try {
          Rename-Item -LiteralPath $f -NewName $aside
          Write-Info "renamed aside $f (in use)"
        } catch {
          Fail "cannot remove $f (it is in use and cannot be renamed)"
        }
      }
    }
  }
  if (Test-Path -LiteralPath $share) {
    Remove-Item -LiteralPath $share -Recurse -Force
    Write-Info "removed $share"
    $existed = $true
  }
  Remove-OldFiles
  if (-not $existed) { Write-Info "nothing to uninstall in $InstallDir" }
}

if ($Uninstall) {
  Invoke-Uninstall
  exit 0
}

Remove-OldFiles

if ($FromDir) {
  # Local source mode: no network, no checksum (npm's own integrity
  # check covers the npm path).
  $src = $null
  if (Test-Path -LiteralPath (Join-Path $FromDir 'netivri.exe')) {
    $src = $FromDir
  } elseif (Test-Path -LiteralPath (Join-Path $FromDir 'bin\netivri.exe')) {
    $src = Join-Path $FromDir 'bin'
  } else {
    Fail "no netivri.exe in $FromDir (looked for $FromDir\netivri.exe and $FromDir\bin\netivri.exe)"
  }
  if (-not (Test-Path -LiteralPath (Join-Path $src $LibName))) {
    Fail "$FromDir does not contain $LibName (looked in $src)"
  }
  Invoke-Install $src '' $FromDir
  exit 0
}

if (-not $Version) {
  if ($BaseUrl) {
    Fail "when -BaseUrl is used, -Version (or NETIVRI_VERSION) must also be given"
  }
  $Version = Get-LatestVersion
  Write-Info "latest release: $Version"
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

  Write-Info "downloading $archiveUrl"
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

  Invoke-Install $src $Version $src
} finally {
  if (Test-Path -LiteralPath $tmp) {
    Remove-Item -LiteralPath $tmp -Recurse -Force
  }
}
