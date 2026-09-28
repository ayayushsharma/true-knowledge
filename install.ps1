# Install tk, then let tk install CBM.
#
# The Windows twin of install.sh, and it exists for the same reason: tk cannot
# replace its own running .exe, so the swap is done by an outside process.
#
# It holds no CBM version input. `tk install cbm` reads the compiled-in pin, so
# there is exactly one place where the CBM version is decided. See
# AGENT_DOCS/history/DECISIONS/2026-09-28-tk-owns-the-binary-and-the-quiesce.md.
#
# Usage:
#   irm https://raw.githubusercontent.com/ayayushsharma/true-knowledge/main/install.ps1 | iex
#   ./install.ps1 v0.1.0
#
# Env:
#   TK_VERSION      tag to install (default: latest release)
#   TK_BIN_DIR      install dir (default: %LOCALAPPDATA%\true-knowledge\bin)
#   TK_NO_CBM       set to skip the CBM install step

[CmdletBinding()]
param([string]$Version = $env:TK_VERSION)

$ErrorActionPreference = 'Stop'
$repo = 'ayayushsharma/true-knowledge'
if (-not $Version) { $Version = 'latest' }
if (-not $env:TK_BIN_DIR) { $env:TK_BIN_DIR = Join-Path $env:LOCALAPPDATA 'true-knowledge\bin' }

$arch = $env:PROCESSOR_ARCHITECTURE
switch ($arch) {
	'AMD64' { $arch = 'amd64' }
	'ARM64' { $arch = 'arm64' }
	default { throw "unsupported architecture: $arch" }
}

if ($Version -eq 'latest') {
	$base = "https://github.com/$repo/releases/latest/download"
	$asset = "tk-windows-$arch.exe"
} else {
	$base = "https://github.com/$repo/releases/download/v$Version"
	$asset = "tk-$Version-windows-$arch.exe"
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("tk-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
	Write-Host "tk $Version ($asset)"
	$binPath = Join-Path $tmp $asset
	Invoke-WebRequest -Uri "$base/$asset" -OutFile $binPath
	$manifest = Join-Path $tmp 'checksums.txt'
	Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $manifest

	# The manifest is the whole trust story, so an asset it does not list is not
	# installed, whatever it downloaded as.
	$want = $null
	foreach ($line in Get-Content $manifest) {
		$parts = $line -split '\s+', 2
		if ($parts.Count -eq 2 -and ($parts[1].Trim() -eq $asset -or $parts[1].Trim() -eq "*$asset")) {
			$want = $parts[0]
			break
		}
	}
	if (-not $want) { throw "$asset is not in checksums.txt - refusing to install an unlisted asset" }
	$got = (Get-FileHash -Algorithm SHA256 -Path $binPath).Hash.ToLower()
	if ($want.ToLower() -ne $got) { throw "checksum mismatch for $asset (manifest $want, got $got)" }

	New-Item -ItemType Directory -Path $env:TK_BIN_DIR -Force | Out-Null
	# Move rather than copy: a .exe cannot be overwritten while it runs, and a
	# rename over the old one is what keeps an open tk session alive.
	Move-Item -Path $binPath -Destination (Join-Path $env:TK_BIN_DIR 'tk.exe') -Force
	Write-Host "installed $env:TK_BIN_DIR\tk.exe"
} finally {
	Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

if (-not $env:TK_NO_CBM) {
	# CBM is tk's own install: it verifies its own release and quiesces the
	# daemon that holds the binary it replaces. This script never picks a CBM
	# version.
	& (Join-Path $env:TK_BIN_DIR 'tk.exe') install cbm
}
