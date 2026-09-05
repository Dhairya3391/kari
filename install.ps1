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
# Detect whether the system architecture is amd64 (x64) or arm64.
# Uses a waterfall of native Windows mechanisms:
#  1. Explicit user override via $env:KARI_ARCH
#  2. Registry (native OS architecture, bypassing 32-bit WOW64 process redirection)
#  3. .NET RuntimeInformation (PowerShell Core / .NET 4.7.1+)
#  4. Native environment variables ($env:PROCESSOR_ARCHITEW6432 / $env:PROCESSOR_ARCHITECTURE)
#  5. CIM / WMI (Win32_Processor / Win32_OperatingSystem)
# If all automatic methods fail or return ambiguous results, prompts the user.

function Detect-Architecture {
  # 1. Override via $env:KARI_ARCH
  if ($env:KARI_ARCH) {
    switch -Regex ($env:KARI_ARCH.Trim()) {
      '^(amd64|x64|x86_64|intel|amd)$' { return 'amd64' }
      '^(arm64|aarch64|arm)$'          { return 'arm64' }
    }
    Fail "Unsupported KARI_ARCH value: $env:KARI_ARCH. Use amd64 or arm64."
  }

  # 2. Registry (Native OS environment, immune to WOW64 emulation)
  try {
    $regArch = (Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment' -Name PROCESSOR_ARCHITECTURE -ErrorAction SilentlyContinue).PROCESSOR_ARCHITECTURE
    if ($regArch) {
      switch -Regex ($regArch.Trim()) {
        '^(AMD64|x64|x86_64)$' { return 'amd64' }
        '^(ARM64|aarch64)$'    { return 'arm64' }
        '^(X86|i[3-6]86)$'      { Fail "32-bit Windows is not supported. kari requires 64-bit Windows (amd64 or arm64)." }
      }
    }
  } catch { }

  # 3. .NET RuntimeInformation (if available on the host runtime)
  try {
    $osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    switch -Regex ($osArch.Trim()) {
      '^(X64|Amd64)$' { return 'amd64' }
      '^(Arm64)$'     { return 'arm64' }
    }
  } catch { }

  # 4. Native Environment Variables (PROCESSOR_ARCHITEW6432 for 32-bit processes on 64-bit OS)
  try {
    $envArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($envArch) {
      switch -Regex ($envArch.Trim()) {
        '^(AMD64|x64|x86_64)$' { return 'amd64' }
        '^(ARM64|aarch64)$'    { return 'arm64' }
        '^(X86|i[3-6]86)$'      { Fail "32-bit Windows is not supported. kari requires 64-bit Windows (amd64 or arm64)." }
      }
    }
  } catch { }

  # 5. CIM / WMI: Win32_Processor (9 = x64, 12 = ARM64)
  try {
    $proc = Get-CimInstance -ClassName Win32_Processor -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $proc) {
      $proc = Get-WmiObject -Class Win32_Processor -ErrorAction SilentlyContinue | Select-Object -First 1
    }
    if ($proc -and $proc.Architecture -ne $null) {
      switch ([int]$proc.Architecture) {
        9  { return 'amd64' }
        12 { return 'arm64' }
      }
    }
  } catch { }

  # 6. CIM / WMI: Win32_OperatingSystem
  try {
    $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $os) {
      $os = Get-WmiObject -Class Win32_OperatingSystem -ErrorAction SilentlyContinue | Select-Object -First 1
    }
    if ($os -and $os.OSArchitecture) {
      # Win32_OperatingSystem.OSArchitecture is bitness ("64-bit"/"32-bit"),
      # not arch — it is "64-bit" on both amd64 and ARM64, so only the ARM
      # branch is trustworthy. A generic "64" match would misclassify ARM64
      # as amd64 when earlier arch-aware detectors have already failed, so on
      # ambiguous 64-bit we return $null to prompt the user instead.
      if ($os.OSArchitecture -match 'ARM\s*64') { return 'arm64' }
      if ($os.OSArchitecture -match '32') { Fail "32-bit Windows is not supported. kari requires 64-bit Windows (amd64 or arm64)." }
    }
  } catch { }

  return $null
}

function Prompt-Architecture {
  Write-Host ""
  Write-Host "Could not automatically detect your processor architecture." -ForegroundColor Yellow
  Write-Host "Please select the architecture for your machine:"
  Write-Host "  [1] amd64 (x64) - Intel or AMD processors (most desktop and laptop PCs)"
  Write-Host "  [2] arm64       - ARM processors (Snapdragon X Elite/Plus, Surface Pro ARM, Copilot+ PCs)"
  Write-Host ""

  try {
    while ($true) {
      $choice = Read-Host "Select architecture [1 for amd64, 2 for arm64]"
      if ([string]::IsNullOrWhiteSpace($choice)) {
        Write-Host "Please choose amd64 or arm64." -ForegroundColor Yellow
        continue
      }
      $choice = $choice.Trim().ToLowerInvariant()
      if ($choice -in @('1', 'amd64', 'x64', 'x86_64', 'amd', 'intel', 'i')) {
        return 'amd64'
      }
      if ($choice -in @('2', 'arm64', 'arm', 'aarch64', 'snapdragon', 'qualcomm', 'a')) {
        return 'arm64'
      }
      Write-Host "Invalid choice '$choice'. Please enter 1 (amd64) or 2 (arm64)." -ForegroundColor Yellow
    }
  } catch {
    Fail "Could not detect processor architecture in a non-interactive shell. Re-run with KARI_ARCH set to amd64 or arm64."
  }
}

function Get-Architecture {
  $detected = Detect-Architecture
  if ($detected) {
    return $detected
  }
  return Prompt-Architecture
}

$Arch = Get-Architecture

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
