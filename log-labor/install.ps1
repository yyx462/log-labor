# install.ps1 — log-labor installer for native Windows (PowerShell 5.1+,
# no Git Bash, no Go, no Node needed).
#
# One-liner (PowerShell):
#   irm https://raw.githubusercontent.com/yyx462/AutoWecom-plugin/master/log-labor/install.ps1 | iex
#
# Same ladder as install.sh, minus the dev rung: download the prebuilt
# release binary for windows/amd64 from GitHub Releases, extract, put it
# on PATH. Git Bash users can keep using install.sh instead.
#
# Env overrides (the one-liner can't take parameters): LOG_LABOR_VERSION,
# LOG_LABOR_BIN_DIR.
$ErrorActionPreference = 'Stop'

# Windows PowerShell 5.1 defaults may not include TLS 1.2.
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {}

# Version resolution, most specific first: env override → GitHub latest
# release → pinned fallback (offline). The fallback MUST stay a real tag.
# (2026-09-15: a stale v0.1.4 default silently downgraded upgrades.)
if ($env:LOG_LABOR_VERSION) {
  $Version = $env:LOG_LABOR_VERSION
} else {
  try {
    $rel = Invoke-RestMethod -Uri 'https://api.github.com/repos/yyx462/AutoWecom-plugin/releases/latest' -TimeoutSec 10
    $Version = if ($rel.tag_name -match '^log-labor-(v[\d.]+)$') { $Matches[1] } else { $null }
  } catch { $Version = $null }
  $Version = if ($Version) { $Version } else { 'v0.1.8' }
}
# win32-x64 is the only prebuilt target; it also runs on ARM64 Windows
# via x64 emulation.
$Arch = 'amd64'
$Tag = "log-labor-$Version"
$Tgz = "log-labor_${Version}_windows_${Arch}.tar.gz"
$Url = "https://github.com/yyx462/AutoWecom-plugin/releases/download/$Tag/$Tgz"
$BinDir = if ($env:LOG_LABOR_BIN_DIR) { $env:LOG_LABOR_BIN_DIR } else { Join-Path $env:USERPROFILE '.local\bin' }

Write-Host "install.ps1: downloading release $Tag (windows/$Arch)..."
$Tmp = Join-Path ([IO.Path]::GetTempPath()) ("log-labor-" + [IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $Tmp | Out-Null
$TgzPath = Join-Path $Tmp $Tgz

# curl.exe ships with Windows 10 1803+ and behaves like the Unix one;
# Invoke-WebRequest is slower on PS 5.1 and trips on some proxy setups.
& curl.exe -fsSL -o "$TgzPath" "$Url"
if ($LASTEXITCODE -ne 0) { throw "download failed: $Url (check https://github.com/yyx462/AutoWecom-plugin/releases for published tags)" }

# gzip magic (1f 8b) check — an HTML error page must not reach tar.
$head = [IO.File]::ReadAllBytes($TgzPath)[0..1]
if ($head[0] -ne 0x1f -or $head[1] -ne 0x8b) { throw "not a gzip tarball (bad download?): $TgzPath" }

# tar.exe (bsdtar) ships with Windows 10 1803+ and understands .tar.gz.
& tar.exe -xzf "$TgzPath" -C "$Tmp"
if ($LASTEXITCODE -ne 0) { throw "tar extract failed (needs Windows 10 1803+ tar.exe)" }

$Src = Join-Path $Tmp 'log-labor.exe'
if (-not (Test-Path $Src)) { throw "tarball did not contain log-labor.exe" }
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
Move-Item -Force "$Src" (Join-Path $BinDir 'log-labor.exe')
Write-Host "installed: $BinDir\log-labor.exe"

# One-step upgrades: refresh agent skills this machine already has
# (idempotent, stamped). Silent best-effort — fresh machines have no
# config yet, and the next-steps block covers the first install.
try { & (Join-Path $BinDir 'log-labor.exe') skill upgrade 2>$null | Out-Null } catch {}

if (($env:Path -split ';') -notcontains $BinDir) {
  Write-Host "NOTE: $BinDir is not on your PATH. Add it via Windows Settings > Environment Variables,"
  Write-Host "      or run once in PowerShell:"
  Write-Host ('      [Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path","User") + ";' + $BinDir + '", "User")')
}

Write-Host ''
Write-Host 'next steps:'
Write-Host '  log-labor init            # webhook key + your corp id (zhang.san form)'
Write-Host '  log-labor doctor          # verify'
Write-Host '  log-labor skill install   # give your agents the skill'
