#!/bin/sh
# Install tk, then let tk install CBM.
#
# This script exists because tk cannot replace its own running binary: on
# Windows the image is locked, and on Linux the running inode survives the
# rename. An external process has neither problem.
#
# It holds no CBM version input. `tk install cbm` reads the compiled-in pin, so
# there is exactly one place where the CBM version is decided. See
# AGENT_DOCS/history/DECISIONS/2026-09-28-tk-owns-the-binary-and-the-quiesce.md.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/ayayushsharma/true-knowledge/main/install.sh | sh
#   ./install.sh v0.1.0
#
# Env:
#   TK_VERSION      tag to install (default: latest release)
#   TK_BIN_DIR      install prefix (default: $HOME/.local/bin)
#   TK_NO_CBM       set to skip the CBM install step

set -eu

REPO="ayayushsharma/true-knowledge"
VERSION="${1:-${TK_VERSION:-latest}}"
BIN_DIR="${TK_BIN_DIR:-$HOME/.local/bin}"

die() { echo "install.sh: $*" >&2; exit 1; }

case "$(uname -s)" in
	Linux) asset="tk-linux-$(uname -m)" ;;
	Darwin) asset="tk-darwin-$(uname -m)" ;;
	*) die "unsupported OS $(uname -s); use install.ps1 on Windows" ;;
esac

if [ "$VERSION" = "latest" ]; then
	base="https://github.com/$REPO/releases/latest/download"
else
	base="https://github.com/$REPO/releases/download/v$VERSION"
	asset="tk-$VERSION-$asset"
fi

# sha256 differs by coreutils vs BSD; both ship one of these.
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "need sha256sum or shasum to verify the download"
fi

fetch() { # fetch <url> <dest>
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		die "need curl or wget"
	fi
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "tk $VERSION ($asset)"
fetch "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

# The manifest is the whole trust story, so an asset it does not list is not
# installed, whatever it downloaded as.
want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1; exit }' "$tmp/checksums.txt")"
[ -n "$want" ] || die "$asset is not in checksums.txt — refusing to install an unlisted asset"
got="$(sha256 "$tmp/$asset")"
[ "$want" = "$got" ] || die "checksum mismatch for $asset (manifest $want, got $got)"

mkdir -p "$BIN_DIR" 2>/dev/null || die "cannot create $BIN_DIR (set TK_BIN_DIR)"
# Install without sudo when the prefix is already ours: a script that needs root
# for a file in the user's own ~/.local/bin trains people to run things as root.
if [ -w "$BIN_DIR" ]; then
	cp "$tmp/$asset" "$BIN_DIR/tk.new" && chmod 755 "$BIN_DIR/tk.new" && mv "$BIN_DIR/tk.new" "$BIN_DIR/tk"
else
	sudo -p '' install -m 755 "$tmp/$asset" "$BIN_DIR/tk" || die "cannot install into $BIN_DIR"
fi

echo "installed $BIN_DIR/tk"
case ":$PATH:" in
	*":$BIN_DIR:"*) ;;
	*) echo "note: $BIN_DIR is not on your PATH" ;;
esac

if [ -n "${TK_NO_CBM:-}" ]; then
	exit 0
fi

# CBM is tk's own install: it verifies its own release and quiesces the daemon
# that holds the binary it replaces. This script never picks a CBM version.
"$BIN_DIR/tk" install cbm
