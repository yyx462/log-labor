#!/usr/bin/env bash
# install.sh — installer for the log-labor CLI.
#
# Public one-liner (downloads a prebuilt release binary, no toolchain):
#   curl -fsSL https://raw.githubusercontent.com/yyx462/AutoWecom-plugin/master/log-labor/install.sh | sh
# Native Windows (PowerShell, no Git Bash needed):
#   irm https://raw.githubusercontent.com/yyx462/AutoWecom-plugin/master/log-labor/install.ps1 | iex
#
# Run from inside a checkout (developers): builds from that source,
# needs `go`. Never touches credentials — `log-labor init` does that.
set -euo pipefail

# Version resolution, most specific first: env override → GitHub latest
# release → pinned fallback. The hardcoded default MUST stay a real,
# existing tag: it is the offline fallback when the API is unreachable.
# (2026-09-15: a stale v0.1.4 default made every env-less `log-labor
# upgrade` silently downgrade — found cutting v0.1.7.)
if [ -n "${LOG_LABOR_VERSION:-}" ]; then
  VERSION="$LOG_LABOR_VERSION"
else
  VERSION="$(curl -fsSL --max-time 10 https://api.github.com/repos/yyx462/AutoWecom-plugin/releases/latest 2>/dev/null \
    | sed -n 's/.*"tag_name": *"log-labor-\(v[0-9.]*\)".*/\1/p' || true)"
  VERSION="${VERSION:-v0.1.9}"
fi
BIN_DIR="${LOG_LABOR_BIN_DIR:-$HOME/.local/bin}"
RELEASES="https://github.com/yyx462/AutoWecom-plugin/releases"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"          # darwin|linux|windows
# Git Bash / MSYS / Cygwin report mingw64_nt-*, msys_nt-*, cygwin_nt-* —
# treat all of those as windows; GOOS handles the rest.
case "$OS" in
  mingw*|msys*|cygwin*) OS=windows ;;
esac
ARCH="$(uname -m)"; [ "$ARCH" = "arm64" ] || [ "$ARCH" = "aarch64" ] && ARCH=arm64 || ARCH=amd64
[ "$OS" = "darwin" ] || [ "$OS" = "linux" ] || [ "$OS" = "windows" ] || { echo "install.sh: unsupported os $OS" >&2; exit 1; }

# On windows the binary needs the .exe suffix, everywhere.
if [ "$OS" = "windows" ]; then
  EXE=".exe"
else
  EXE=""
fi

TAG="log-labor-${VERSION}"
TGZ="log-labor_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="$RELEASES/download/$TAG/$TGZ"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# gzip magic (1f8b) check; xxd is absent from some minimal Git Bash installs.
is_gzip() { head -c2 "$1" | od -An -tx1 | tr -d ' \n' | grep -q '^1f8b'; }

echo "install.sh: looking for a source…"
SRC=""
HERE="$(cd "$(dirname "$0")" && pwd)"
# Local-checkout rung: only when this script really lives in the repo —
# a piped `curl | sh` has no $0 file and must not build whatever Go
# project happens to be in the caller's working directory.
if [ -f "$HERE/go.mod" ] && [ -d "$HERE/cmd/log-labor" ]; then
  echo "install.sh: building from the local checkout at $HERE"
  command -v go >/dev/null || { echo "install.sh: FATAL — go not found; install go, or run the one-liner outside a checkout to fetch a release binary" >&2; exit 1; }
  ( cd "$HERE" && go build -o "$TMP/log-labor$EXE" ./cmd/log-labor )
  SRC="$TMP/log-labor$EXE"
fi
if [ -z "$SRC" ]; then
  echo "install.sh: downloading release $TAG ($OS/$ARCH)…"
  if curl -fsSL -m 60 -o "$TMP/$TGZ" "$URL" && is_gzip "$TMP/$TGZ"; then
    tar -xzf "$TMP/$TGZ" -C "$TMP" && SRC="$TMP/log-labor$EXE"
  fi
fi
if [ -z "$SRC" ]; then
  echo "install.sh: FATAL — no prebuilt binary for $OS/$ARCH:" >&2
  echo "  $URL" >&2
  echo "  check $RELEASES for published tags (or install go and run from a checkout)" >&2
  exit 1
fi

mkdir -p "$BIN_DIR"
if [ -w "$BIN_DIR" ]; then
  mv "$SRC" "$BIN_DIR/log-labor$EXE"
elif [ "$OS" = "windows" ]; then
  echo "install.sh: FATAL — $BIN_DIR not writable" >&2; exit 1
else
  echo "install.sh: $BIN_DIR not writable — using sudo"
  sudo mv "$SRC" "$BIN_DIR/log-labor$EXE"
fi
[ "$OS" = "windows" ] || chmod +x "$BIN_DIR/log-labor$EXE"
echo "installed: $BIN_DIR/log-labor$EXE"

# One-step upgrades: refresh agent skills this machine already has
# (idempotent, stamped). Silent best-effort — fresh machines have no
# config yet, and the next-steps block covers the first install.
"$BIN_DIR/log-labor$EXE" skill upgrade >/dev/null 2>&1 || true

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    if [ "$OS" = "windows" ]; then
      echo "NOTE: $BIN_DIR is not on your PATH — add it in ~/.bashrc (export PATH=\"$BIN_DIR:\$PATH\"),"
      echo "      or add %USERPROFILE%\\.local\\bin via Windows Settings > Environment Variables for non-bash shells"
    else
      echo "NOTE: $BIN_DIR is not on your PATH — add it (e.g. in ~/.zshrc: export PATH=\"$BIN_DIR:\$PATH\")"
    fi
    ;;
esac

cat <<'NEXT'

next steps:
  log-labor init            # webhook key + your corp id (zhang.san form)
  log-labor doctor          # verify
  log-labor skill install   # give your agents the skill
NEXT
