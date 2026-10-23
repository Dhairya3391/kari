#!/usr/bin/env bash
# install.sh — kari installer for macOS, Linux, and Android (Termux).
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Dhairya3391/kari/main/install.sh | bash
#   KARI_INSTALL_DIR=/usr/local/bin bash install.sh
set -euo pipefail

REPO="Dhairya3391/kari"
BASE_URL="https://github.com/${REPO}/releases/latest/download"

# ── helpers ──────────────────────────────────────────────────────────────────
info()    { printf '\033[1m%s\033[0m\n' "$*"; }
step()    { printf '  \033[32m▸\033[0m %s\n' "$*"; }
warn()    { printf '  \033[33m!\033[0m %s\n' "$*" >&2; }
error()   { printf '\033[31mError:\033[0m %s\n' "$*" >&2; exit 1; }

# ── 1. Prerequisites ──────────────────────────────────────────────────────────
if ! command -v curl >/dev/null 2>&1; then
  if command -v wget >/dev/null 2>&1; then
    # Thin shim so the rest of the script uses a single download function.
    curl() {
      local url="" out=""
      while [ $# -gt 0 ]; do
        case "$1" in
          -o) shift; out="$1" ;;
          http*) url="$1" ;;
        esac
        shift
      done
      wget -q -O "$out" "$url"
    }
  else
    error "Neither curl nor wget is available. Install one and re-run."
  fi
fi

# ── 2. Detect OS / arch ───────────────────────────────────────────────────────
detect_os() {
  # Termux sets TERMUX_VERSION; check before uname to avoid false linux match.
  if [ -n "${TERMUX_VERSION:-}" ]; then echo "android"; return; fi
  case "$(uname -s)" in
    Linux*)             echo "linux"   ;;
    Darwin*)            echo "darwin"  ;;
    CYGWIN*|MINGW*|MSYS*) echo "windows" ;;
    *)                  echo "unknown" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)   echo "amd64" ;;
    arm64|aarch64)  echo "arm64" ;;
    *)              echo "unknown" ;;
  esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

case "$OS" in
  windows)
    error "This script is for macOS / Linux / Termux.
  On Windows run instead:
    irm https://raw.githubusercontent.com/${REPO}/main/install.ps1 | iex"
    ;;
  unknown)
    error "Unsupported OS: $(uname -s). kari ships binaries for linux, darwin, windows, and android (Termux)."
    ;;
esac

if [ "$ARCH" = "unknown" ]; then
  error "Unsupported architecture: $(uname -m). kari ships amd64 and arm64 builds only."
fi

if [ "$OS" = "android" ] && [ "$ARCH" != "arm64" ]; then
  error "kari only publishes an android-arm64 build; detected arch $(uname -m) is not supported on Termux."
fi

ASSET="kari-${OS}-${ARCH}"

# ── 3. Install directory ──────────────────────────────────────────────────────
if [ "$OS" = "android" ]; then
  DEFAULT_INSTALL_DIR="${PREFIX:-/data/data/com.termux/files/usr}/bin"
else
  DEFAULT_INSTALL_DIR="$HOME/.local/bin"
fi
INSTALL_DIR="${KARI_INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"

# Sanity-check $HOME is set before we try to mkdir under it.
[ -n "${HOME:-}" ] || error "\$HOME is not set; cannot determine install directory."

mkdir -p "$INSTALL_DIR" || error "Cannot create install directory: ${INSTALL_DIR}"

# ── 4. Download binary (atomic) ───────────────────────────────────────────────
DEST="$INSTALL_DIR/kari"
TMP="$DEST.tmp.$$"
DOWNLOAD_URL="${BASE_URL}/${ASSET}"

cleanup() { rm -f "$TMP"; }
trap cleanup EXIT

info ""
info "Installing kari"
step "Platform: ${OS}/${ARCH}"
step "Destination: ${DEST}"
step "Downloading ${DOWNLOAD_URL} ..."

if ! curl -fL --progress-bar -o "$TMP" "$DOWNLOAD_URL"; then
  error "Download failed.
  Asset '${ASSET}' may not exist in the latest release, or you are offline.
  URL: ${DOWNLOAD_URL}"
fi

# ── 4a. Checksum verification (best-effort) ───────────────────────────────────
SHA_URL="${DOWNLOAD_URL}.sha256"
SHA_TMP="${TMP}.sha256"
if curl -fsSL -o "$SHA_TMP" "$SHA_URL" 2>/dev/null; then
  EXPECTED_HASH="$(awk '{print $1}' "$SHA_TMP")"
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL_HASH="$(sha256sum "$TMP" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL_HASH="$(shasum -a 256 "$TMP" | awk '{print $1}')"
  else
    ACTUAL_HASH=""
  fi
  if [ -n "$ACTUAL_HASH" ]; then
    if [ "$ACTUAL_HASH" = "$EXPECTED_HASH" ]; then
      step "Checksum verified ✓"
    else
      rm -f "$SHA_TMP"
      error "Checksum mismatch — download may be corrupt or tampered.
  Expected: ${EXPECTED_HASH}
  Got:      ${ACTUAL_HASH}"
    fi
  fi
  rm -f "$SHA_TMP"
fi

chmod +x "$TMP"
mv -f "$TMP" "$DEST"
trap - EXIT

# macOS Gatekeeper quarantine — strip defensively; no-op when attribute absent.
if [ "$OS" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$DEST" 2>/dev/null || true
fi

# ── 4b. Warn if a different kari is already on PATH ───────────────────────────
EXISTING="$(command -v kari 2>/dev/null || true)"
if [ -n "$EXISTING" ] && [ "$EXISTING" != "$DEST" ]; then
  warn "Another kari binary exists at ${EXISTING}"
  warn "It will shadow the newly installed one until your PATH is updated."
fi

# ── 5. PATH handling ──────────────────────────────────────────────────────────
path_has_dir() {
  case ":${PATH}:" in
    *":$1:"*) return 0 ;;
    *) return 1 ;;
  esac
}

ALREADY_ON_PATH=0
PATH_UPDATED=0
ALREADY_IN_RC=0
RC=""

if path_has_dir "$INSTALL_DIR"; then
  ALREADY_ON_PATH=1
else
  # Use $SHELL (the user's login shell, not the script's executor) to find
  # the right rc file — curl | bash always runs bash regardless of login shell.
  SHELL_NAME="$(basename "${SHELL:-sh}")"

  case "$SHELL_NAME" in
    fish)
      RC="$HOME/.config/fish/config.fish"
      # fish_add_path is Fish 3.2+; fall back to set -Ux for older versions.
      if fish -c 'fish_add_path --help' >/dev/null 2>&1; then
        LINE="fish_add_path \"$INSTALL_DIR\""
      else
        LINE="set -Ux fish_user_paths \"$INSTALL_DIR\" \$fish_user_paths"
      fi
      ;;
    zsh)
      RC="${ZDOTDIR:-$HOME}/.zshrc"
      LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
      ;;
    bash)
      # macOS Terminal.app launches login shells; .bashrc isn't sourced.
      if [ "$OS" = "darwin" ]; then
        RC="$HOME/.bash_profile"
      else
        RC="$HOME/.bashrc"
      fi
      LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
      ;;
    *)
      RC="$HOME/.profile"
      LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
      ;;
  esac

  mkdir -p "$(dirname "$RC")" 2>/dev/null || true
  touch "$RC" 2>/dev/null || true

  if grep -qxF "$LINE" "$RC" 2>/dev/null; then
    ALREADY_IN_RC=1
  elif [ -w "$RC" ]; then
    printf '\n# Added by kari installer\n%s\n' "$LINE" >> "$RC"
    PATH_UPDATED=1
  fi
fi

# ── 6. Man page (best-effort; skipped on Android/Termux) ─────────────────────
MAN_INSTALLED=0
if [ "$OS" != "android" ]; then
  MAN_PAGE_URL="https://raw.githubusercontent.com/${REPO}/main/docs/kari.1"

  # Try system path first (/usr/local/share/man/man1), then user-local path (~/.local/share/man/man1).
  MAN_DIR="/usr/local/share/man/man1"
  if [ ! -d "$MAN_DIR" ]; then
    mkdir -p "$MAN_DIR" 2>/dev/null || sudo mkdir -p "$MAN_DIR" 2>/dev/null || true
  fi

  if [ ! -w "$MAN_DIR" ]; then
    MAN_DIR="$HOME/.local/share/man/man1"
    mkdir -p "$MAN_DIR" 2>/dev/null || true
  fi

  MAN_DEST="${MAN_DIR}/kari.1"

  if [ -w "$MAN_DIR" ]; then
    if curl -fsSL -o "$MAN_DEST" "$MAN_PAGE_URL" 2>/dev/null; then
      MAN_INSTALLED=1
      # Linux only: rebuild the whatis database. macOS reads man files directly.
      [ "$OS" = "linux" ] && command -v mandb >/dev/null 2>&1 && mandb -q 2>/dev/null || true
    else
      warn "Could not fetch man page (offline?). Install later with:"
      warn "  sudo mkdir -p /usr/local/share/man/man1 && sudo cp docs/kari.1 /usr/local/share/man/man1/kari.1"
    fi
  else
    warn "No writable man directory found. Install the man page manually:"
    warn "  sudo mkdir -p /usr/local/share/man/man1 && sudo cp docs/kari.1 /usr/local/share/man/man1/kari.1"
  fi
fi

# ── 7. Verify & report ────────────────────────────────────────────────────────
VERSION_OUTPUT="$("$DEST" --version 2>/dev/null || true)"

info ""
info "kari installed successfully!"
[ -n "$VERSION_OUTPUT" ] && step "Version:  ${VERSION_OUTPUT}"
step "Location: ${DEST}"
[ "$MAN_INSTALLED" -eq 1 ] && step "Man page: ${MAN_DEST}  (run 'man kari')"

if [ "$ALREADY_ON_PATH" -eq 1 ]; then
  info ""
  info "Run 'kari' to get started."
elif [ "$PATH_UPDATED" -eq 1 ]; then
  info ""
  step "Added ${INSTALL_DIR} to PATH in ${RC}."
  info "Restart your terminal, or run:"
  info "  source ${RC}"
  info "then type 'kari' to launch."
elif [ "$ALREADY_IN_RC" -eq 1 ]; then
  info ""
  step "${INSTALL_DIR} is already in ${RC} from a previous install."
  info "Restart your terminal (or 'source ${RC}') if kari isn't found yet."
else
  info ""
  warn "${INSTALL_DIR} is not on your PATH and the shell config could not be updated."
  warn "Add it manually:"
  warn "  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi
