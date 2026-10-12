#!/usr/bin/env bash
# vendor-cbm-android.sh — fetch codebase-memory-mcp source and build it on Android.
#
# Android (Termux) is a DEV machine for tk, never a deployment target: tk still
# ships Linux and macOS, and upstream publishes no Android CBM binary (a glibc
# release cannot execute on bionic). So `tk install cbm` has no Android asset
# by design, and this script is the dev-only path: clone the pinned tag into
# third_party/codebase-memory-mcp, apply patches/cbm-android/*.patch, verify
# the tree stays invisible to tk's own tooling, and build with CBM's scripts/build.sh.
#
# The upstream tree is script-owned and never tracked: /third_party/ is
# gitignored (which also hides it from rg), .cbmignore keeps it out of CBM
# indexing, and its nested go.mod keeps the go tool out of it for ./....
# (--check re-verifies the docs-lint/gofmt invisibility per pin.)
# It is NOT ./vendor/: the go tool takes a top-level vendor/ as a module
# vendor directory and fails every command with "inconsistent vendoring"
# unless vendor/modules.txt pins all of go.mod (probed).
#
# Usage:
#   bash scripts/vendor-cbm-android.sh [--pin X.Y.Z] [--reset] [--check-only]
#                                      [--install-deps] [--allow-foreign]
#
#   --pin X        CBM version to check out (default: tk's DefaultCBMPin).
#   --reset        Discard local changes in the upstream tree and rebuild clean.
#   --check-only   Fetch + patch + verify, skip the build.
#   --install-deps `pkg install` the missing build prerequisites (Termux).
#   --allow-foreign
#                  Bypass the Android-only gate (Linux glibc test runs).
#
# After a successful build, point tk at the binary (per shell, or persisted):
#   export TK_CBM_BIN="$PWD/third_party/codebase-memory-mcp/build/c/codebase-memory-mcp"
#   tk config set cbm_binary "$TK_CBM_BIN"

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TREE_DIR="$REPO_ROOT/third_party/codebase-memory-mcp"
PATCH_DIR="$REPO_ROOT/patches/cbm-android"
UPSTREAM="https://github.com/DeusData/codebase-memory-mcp"

PIN=""
RESET=0
CHECK_ONLY=0
INSTALL_DEPS=0
ALLOW_FOREIGN=0

for arg in "$@"; do
    case "$arg" in
        --pin=*) PIN="${arg#--pin=}" ;;
        --pin) echo "usage: --pin=X.Y.Z" >&2; exit 2 ;;
        --reset) RESET=1 ;;
        --check-only) CHECK_ONLY=1 ;;
        --install-deps) INSTALL_DEPS=1 ;;
        --allow-foreign) ALLOW_FOREIGN=1 ;;
        -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
        *) echo "vendor-cbm-android.sh: unknown option '$arg'" >&2; exit 2 ;;
    esac
done
# All options are self-contained (--pin=X, bare flags), so no shifting.

if [[ -z "$PIN" ]]; then
    # No `| head` here: under `set -o pipefail` an early-exiting head SIGPIPEs
    # grep and fails the whole pipeline. Take the first line instead.
    PIN="$(grep -h 'DefaultCBMPin = "' "$REPO_ROOT"/internal/config/*.go \
        | sed -n 's/.*DefaultCBMPin = "\([^"]*\)".*/\1/p' || true)"
    PIN="${PIN%%$'\n'*}"
    if [[ -z "$PIN" ]]; then
        echo "vendor-cbm-android.sh: cannot read DefaultCBMPin from internal/config; pass --pin=X.Y.Z" >&2
        exit 1
    fi
fi
TAG="v$PIN"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1"; return 1; }; }

# ── Android-only gate ──────────────────────────────────────────────
# The one property that makes this tree Android: the compiler targets bionic.
CC_CANDIDATE="${CC:-gcc}"
if [[ "$ALLOW_FOREIGN" != "1" ]]; then
    # grep -c, not grep -q: under `set -o pipefail` an early-exiting grep -q
    # SIGPIPEs the compiler and fails the pipeline even on a match (flake).
    is_android="$(echo | "$CC_CANDIDATE" -dM -E - 2>/dev/null | grep -c 'define __ANDROID__ ' || true)"
    if [[ "${is_android:-0}" -eq 0 ]]; then
        echo "vendor-cbm-android.sh: refusing: '$CC_CANDIDATE' does not target Android (__ANDROID__ not predefined)." >&2
        echo "This script is Android-only (Termux). Deployment targets stay Linux/macOS." >&2
        echo "Override for a glibc test run with --allow-foreign." >&2
        exit 1
    fi
fi

# ── Prerequisites ──────────────────────────────────────────────────
missing=()
for tool in git make "$CC_CANDIDATE"; do
    need "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
if [[ "${#missing[@]}" -gt 0 ]]; then
    if [[ "$INSTALL_DEPS" == "1" ]] && [[ -n "${PREFIX:-}" ]] && command -v pkg >/dev/null 2>&1; then
        pkg install -y "${missing[@]}"
    else
        echo "vendor-cbm-android.sh: missing build tools: ${missing[*]}" >&2
        echo "Termux: pkg install ${missing[*]}   (or re-run with --install-deps)" >&2
        exit 1
    fi
fi

# ── Fetch ──────────────────────────────────────────────────────────
if [[ ! -d "$TREE_DIR/.git" ]]; then
    echo "cloning $UPSTREAM@$TAG into third_party/ (untracked; ~100MB, one tag, shallow)..."
    mkdir -p "$REPO_ROOT/third_party"
    git clone --depth 1 --branch "$TAG" "$UPSTREAM" "$TREE_DIR"
    # A shallow --branch checkout of a tag leaves no tag ref behind, so plant
    # one: later runs use `describe --exact-match` to skip the fetch.
    git -C "$TREE_DIR" fetch --depth 1 origin tag "$TAG"
else
    cur="$(git -C "$TREE_DIR" describe --tags --exact-match 2>/dev/null || true)"
    if [[ "$RESET" == "1" ]]; then
        echo "resetting upstream tree to $TAG..."
        git -C "$TREE_DIR" fetch --depth 1 origin tag "$TAG"
        git -C "$TREE_DIR" checkout -q -f "$TAG"
        git -C "$TREE_DIR" clean -fdq
    elif [[ "$cur" != "$TAG" ]]; then
        # HEAD must move: refuse to do that over local changes.
        if ! git -C "$TREE_DIR" diff --quiet || ! git -C "$TREE_DIR" diff --cached --quiet; then
            echo "vendor-cbm-android.sh: upstream tree is dirty; commit nothing there (it is untracked) — re-run with --reset to discard." >&2
            exit 1
        fi
        echo "updating upstream tree to $TAG..."
        git -C "$TREE_DIR" fetch --depth 1 origin tag "$TAG"
        git -C "$TREE_DIR" checkout -q -f "$TAG"
    fi
fi

# ── Patch ──────────────────────────────────────────────────────────
shopt -s nullglob
patches=("$PATCH_DIR"/*.patch)
if [[ "${#patches[@]}" -eq 0 ]]; then
    echo "vendor-cbm-android.sh: no patches in $PATCH_DIR; nothing to apply." >&2
    exit 1
fi
for p in "${patches[@]}"; do
    echo "checking $(basename "$p")..."
    if git -C "$TREE_DIR" apply --check --reverse "$p" 2>/dev/null; then
        echo "  already applied; skipping."
        continue
    fi
    git -C "$TREE_DIR" apply --check "$p" || {
        echo "vendor-cbm-android.sh: $(basename "$p") applies neither forward nor reverse to $TAG." >&2
        echo "The pin moved past the patch set (refresh patches/ against the new tag), or the tree" >&2
        echo "has hand edits colliding with it — re-run with --reset to discard." >&2
        exit 1
    }
    echo "  applying."
    git -C "$TREE_DIR" apply "$p"
done
if ! git -C "$TREE_DIR" diff --quiet || ! git -C "$TREE_DIR" diff --cached --quiet; then
    echo "note: upstream tree carries changes beyond the patch set; keeping them for this run."
fi

# ── Invisibility check: the upstream tree must not trip tk's own gates ──
# docs-lint (internal/docs) scans .md/.go/.sh/.json/.toml repo-wide outside
# history/ for pre-consolidation paths; gofmt -l . walks everything.
# The pattern is assembled from $p so no single line of THIS file contains a
# full pre-consolidation path (internal/docs scans .sh too).
p="docs"
forbidden="$p/DECISIONS/|$p/00-AUTHORITY\\.md|$p/AGENT-PROFILES\\.md|$p/CBM-BOUNDARY\\.md|$p/INDEXING\\.md|$p/PATHS-CONFIG\\.md|$p/REMAINING-WORK\\.md|$p/ROADMAP\\.md"
hits="$(grep -rln -E "$forbidden" "$TREE_DIR" --include='*.md' --include='*.go' \
    --include='*.sh' --include='*.json' --include='*.toml' --include='*.jsonc' 2>/dev/null || true)"
if [[ -n "$hits" ]]; then
    echo "vendor-cbm-android.sh: upstream tree mentions a pre-consolidation doc path; docs-lint would fail." >&2
    printf '%s\n' "$hits" | head -5 >&2
    exit 1
fi
if [[ -n "$(gofmt -l "$TREE_DIR" 2>/dev/null)" ]]; then
    echo "vendor-cbm-android.sh: upstream .go files are not gofmt-clean; 'gofmt -l .' would fail." >&2
    gofmt -l "$TREE_DIR" >&2
    exit 1
fi
echo "invisibility check passed (docs-lint paths, gofmt)."

if [[ "$CHECK_ONLY" == "1" ]]; then
    echo "fetch + patch + verify done; build skipped (--check-only)."
    exit 0
fi

# ── Build ──────────────────────────────────────────────────────────
# Always dynamic on Android: bionic ships no static libc (patch 0001 turns a
# cryptic lld failure into an explicit error if STATIC=1 leaks in).
echo "building CBM $TAG for Android (dynamic; this takes a while on-device)..."
env -u STATIC bash "$TREE_DIR/scripts/build.sh" --version "$TAG"
BIN="$TREE_DIR/build/c/codebase-memory-mcp"
if [[ ! -x "$BIN" ]]; then
    echo "vendor-cbm-android.sh: build finished but $BIN is missing." >&2
    exit 1
fi
"$BIN" --version 2>&1 | head -2 || true
echo
echo "built: $BIN"
echo "use it for this shell:  export TK_CBM_BIN=\"$BIN\""
echo "or persist it:          tk config set cbm_binary \"$BIN\""
