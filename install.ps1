$ErrorActionPreference = 'Stop'

$Repo    = 'Dhairya3391/kari'
$BaseUrl = "https://github.com/$Repo/releases/latest/download"

function Write-Info($msg) { Write-Host $msg }
function Fail($msg) { Write-Error $msg; exit 1 }

# Force TLS 1.2+ -- some Windows PowerShell 5.1 hosts default to TLS 1.0/1.1,
# which GitHub rejects, causing a confusing connection failure.
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

# --- 1. Arch detection ---
# IMPORTANT: use OSArchitecture, not ProcessArchitecture. ProcessArchitecture
# reports the bitness of the *current PowerShell process*, which lies under
# WOW64 (a 32-bit powershell.exe on 64-bit Windows reports X86) -- the same
# failure mode as $env:PROCESSOR_ARCHITECTURE. OSArchitecture reports the
# actual OS/hardware architecture regardless of host process bitness.
$osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
switch ($osArch) {
  'X64'   { $Arch = 'amd64' }
  'Arm64' { $Arch = 'arm64' }
  default { Fail "Unsupported architecture: $osArch. kari ships amd64 and arm64 Windows builds only." }
}

$AssetName   = "kari-windows-$Arch.exe"
$DownloadUrl = "$BaseUrl/$AssetName"

# --- 2. Install dir (override with $env:KARI_INSTALL_DIR) ---
$InstallDir = if ($env:KARI_INSTALL_DIR) { $env:KARI_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\kari' }
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$Dest = Join-Path $InstallDir 'kari.exe'
$Tmp  = "$Dest.tmp"

# --- 3. Download (atomic install) ---
Write-Info "Downloading kari (windows/$Arch)..."
Write-Info "  $DownloadUrl"

try {
  Invoke-WebRequest -Uri $DownloadUrl -OutFile $Tmp -UseBasicParsing
} catch {
  Remove-Item -Force -ErrorAction SilentlyContinue $Tmp
  Fail "Download failed: $($_.Exception.Message)`nURL: $DownloadUrl"
}

try {
  Move-Item -Force -Path $Tmp -Destination $Dest
} catch {
  Remove-Item -Force -ErrorAction SilentlyContinue $Tmp
  Fail "Could not replace $Dest ($($_.Exception.Message)). If kari.exe is currently running, close it and re-run this script."
}

# Files downloaded via Invoke-WebRequest get tagged with a Zone.Identifier
# alternate data stream (the "Mark of the Web"), which can trigger a
# SmartScreen warning when the exe is run. Unblock-File strips it.
Unblock-File -Path $Dest -ErrorAction SilentlyContinue

Write-Info "Installed kari to $Dest"

# --- 4. PATH handling (User scope only, no admin needed) ---
$UserPath    = [Environment]::GetEnvironmentVariable('Path', 'User')
$PathEntries = @()
if ($UserPath) { $PathEntries = $UserPath -split ';' | Where-Object { $_ -ne '' } }

$PathUpdated = $false
if ($PathEntries -notcontains $InstallDir) {
  $NewUserPath = if ($UserPath) { "$UserPath;$InstallDir" } else { $InstallDir }
  [Environment]::SetEnvironmentVariable('Path', $NewUserPath, 'User')
  $PathUpdated = $true
}

# Make it usable in *this* session immediately, without opening a new terminal.
$SessionEntries = $env:Path -split ';' | Where-Object { $_ -ne '' }
if ($SessionEntries -notcontains $InstallDir) {
  $env:Path = "$env:Path;$InstallDir"
}

# --- 5. Verify & report ---
$VersionOutput = $null
try { $VersionOutput = & $Dest -v 2>$null } catch { }

Write-Info ""
Write-Info "kari installed successfully!"
if ($VersionOutput) { Write-Info "Version:  $VersionOutput" }
Write-Info "Location: $Dest"

if ($PathUpdated) {
  Write-Info ""
  Write-Info "$InstallDir was added to your user PATH."
  Write-Info "It's active in this window already; new terminals will pick it up automatically."
} else {
  Write-Info "Run 'kari' to get started."
}
