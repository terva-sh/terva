#!/usr/bin/env bash
#
# terva installer — downloads from the project's release host (see the
# channel-identity block below; never from upstream).
#
# Usage:
#   curl -fsSL https://terva.sh/install.sh | bash
#   curl -fsSL https://terva.sh/install.sh | bash -s -- v0.0.1 ~/bin
#
# Positional arguments:
#   $1  version    — release tag (e.g. v0.0.1). Defaults to "latest".
#   $2  prefix     — install directory. Defaults to the first writable
#                    directory in: /usr/local/bin, $HOME/.local/bin,
#                    $HOME/bin. Created if missing. Add it to your PATH
#                    if it isn't already.
#
# Environment overrides:
#   TERVA_VERSION    same as $1
#   TERVA_PREFIX     same as $2
#   An access token for the release host (the variable named by
#   TOKEN_VAR below) — required only while the repo is private; needs
#   read access to the terva repository.
#
# The script detects your OS and architecture, downloads the matching
# archive from the release, verifies the sha256 against the release's
# checksums.txt, extracts the binary, and moves it into the prefix
# directory. No sudo unless you explicitly pick a prefix that needs it.

set -euo pipefail

# --- channel identity (the release-cut rewrites this block; scripts/release.sh) ---
HOST="https://github.com"
OWNER="terva-sh"
REPO="terva"
API_LATEST="https://api.github.com/repos/${OWNER}/${REPO}/releases/latest"
TOKEN_VAR="GITHUB_TOKEN"
# --- end channel identity ---

BINARY="terva"

VERSION="${1:-${TERVA_VERSION:-latest}}"
PREFIX="${2:-${TERVA_PREFIX:-}}"

msg()  { printf "\033[1m==>\033[0m %s\n" "$*"; }
warn() { printf "\033[33mwarn:\033[0m %s\n" "$*" >&2; }
die()  { printf "\033[31merror:\033[0m %s\n" "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"

# CURL_AUTH is prepended to every curl invocation so private-repo
# downloads work while the channel's token variable is set. Empty
# array when the repo is public. The token variable's NAME comes from
# the channel block ($TOKEN_VAR); ${!TOKEN_VAR} reads its value —
# supported on bash 3.2.
#
# Note the "${CURL_AUTH[@]+"${CURL_AUTH[@]}"}" pattern at every call
# site: bash 3.2 (the default /bin/bash on macOS) treats an
# unquoted empty-array expansion as an unbound variable under
# `set -u` and aborts with "CURL_AUTH[@]: unbound variable". The
# guard expands to nothing when the array is empty and to the
# array's contents when it isn't. Bash 4+ doesn't need this, but
# the installer's primary audience is `curl | bash` on macOS.
TOKEN="${!TOKEN_VAR:-}"
CURL_AUTH=()
if [ -n "$TOKEN" ]; then
  CURL_AUTH=(-H "Authorization: Bearer $TOKEN")
fi

# ---- detect OS + arch ----

uname_s=$(uname -s)
uname_m=$(uname -m)

case "$uname_s" in
  Linux)   OS=linux ;;
  Darwin)  OS=darwin ;;
  MINGW*|MSYS*|CYGWIN*)
    die "windows detected — use install.ps1 from powershell instead"
    ;;
  *) die "unsupported os: $uname_s" ;;
esac

case "$uname_m" in
  x86_64|amd64)         ARCH=amd64 ;;
  arm64|aarch64)        ARCH=arm64 ;;
  *) die "unsupported arch: $uname_m" ;;
esac

# ---- resolve version ----

if [ "$VERSION" = "latest" ]; then
  # Forgejo's release API carries the same tag_name field GitHub's
  # does, and works with or without auth, so use it unconditionally.
  VERSION=$(curl -fsSL "${CURL_AUTH[@]+"${CURL_AUTH[@]}"}" \
    "$API_LATEST" \
    | sed -nE 's/.*"tag_name": *"([^"]+)".*/\1/p' | head -n1)
  [ -n "$VERSION" ] || die "could not resolve latest version (set $TOKEN_VAR if the repo is private)"
fi

case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac
VER_NUM="${VERSION#v}"

# ---- pick an install prefix ----

pick_prefix() {
  local candidates=()
  [ -n "$PREFIX" ] && { echo "$PREFIX"; return; }
  candidates+=("/usr/local/bin")
  [ -n "${HOME:-}" ] && candidates+=("$HOME/.local/bin" "$HOME/bin")
  for d in "${candidates[@]}"; do
    if [ -d "$d" ] && [ -w "$d" ]; then
      echo "$d"
      return
    fi
  done
  # Nothing writable yet — create ~/.local/bin and use that.
  if [ -n "${HOME:-}" ]; then
    mkdir -p "$HOME/.local/bin"
    echo "$HOME/.local/bin"
    return
  fi
  die "no writable install prefix found; pass one as the second argument"
}

PREFIX=$(pick_prefix)
mkdir -p "$PREFIX"

# ---- download + verify + extract ----

ARCHIVE="${BINARY}_${VER_NUM}_${OS}_${ARCH}.tar.gz"
BASE_URL="${HOST}/${OWNER}/${REPO}/releases/download/${VERSION}"
ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
CHECKSUMS_URL="${BASE_URL}/checksums.txt"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

msg "downloading ${ARCHIVE}"
curl -fsSL "${CURL_AUTH[@]+"${CURL_AUTH[@]}"}" -o "$TMP/$ARCHIVE" "$ARCHIVE_URL" \
  || die "download failed: $ARCHIVE_URL (set $TOKEN_VAR if the repo is private)"

msg "verifying checksum"
curl -fsSL "${CURL_AUTH[@]+"${CURL_AUTH[@]}"}" -o "$TMP/checksums.txt" "$CHECKSUMS_URL" \
  || die "download failed: $CHECKSUMS_URL"

expected=$(grep " ${ARCHIVE}\$" "$TMP/checksums.txt" | awk '{print $1}' || true)
[ -n "$expected" ] || die "no checksum for $ARCHIVE in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')
else
  die "no sha256 tool found (sha256sum or shasum)"
fi

[ "$expected" = "$actual" ] \
  || die "checksum mismatch: expected $expected, got $actual"

msg "extracting"
tar -xzf "$TMP/$ARCHIVE" -C "$TMP"

[ -f "$TMP/$BINARY" ] || die "archive did not contain a '$BINARY' binary"

msg "installing to $PREFIX/$BINARY"
install -m 0755 "$TMP/$BINARY" "$PREFIX/$BINARY" 2>/dev/null \
  || { cp "$TMP/$BINARY" "$PREFIX/$BINARY" && chmod 0755 "$PREFIX/$BINARY"; }

# ---- rename compat ----
# terva was formerly zot; keep muscle memory and old scripts # rename:keep
# working for a release cycle with a compat symlink, unless a # rename:keep
# real zot binary (not ours) already sits there. # rename:keep
if [ ! -e "$PREFIX/zot" ] || [ -L "$PREFIX/zot" ]; then # rename:keep
  { ln -sf "$PREFIX/$BINARY" "$PREFIX/zot" && msg "compat symlink: $PREFIX/zot -> $BINARY"; } 2>/dev/null || true # rename:keep
fi

# ---- PATH hint ----

case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *)
    warn "$PREFIX is not on your PATH"
    warn "add this to your shell rc file:"
    warn "  export PATH=\"$PREFIX:\$PATH\""
    ;;
esac

msg "installed $("$PREFIX/$BINARY" --version 2>/dev/null || echo terva)"
msg "run:  terva          (interactive tui)"
msg "run:  terva --help   (all flags and subcommands)"
