#!/usr/bin/env bash
set -euo pipefail

REPO="Dhairya3391/kari"
BASE_URL="https://github.com/${REPO}/releases/latest/download"

info()  { printf '%s\n' "$*"; }
error() { printf 'Error: %s\n' "$*" >&2; exit 1; }

# --- 1. Detect OS/arch (mirrors build.sh's get_host_os/get_host_arch) ---
detect_os() {
  if [ -n "${TERMUX_VERSION:-}" ]; then
    echo "android"
    return
  fi
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    CYGWIN*|MINGW*|MSYS*) echo "windows" ;;
    *) echo "unknown" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) echo "unknown" ;;
  esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

case "$OS" in
  windows)
    error "This script is for macOS/Linux/Termux. On Windows, run instead:
  irm https://raw.githubusercontent.com/${REPO}/main/install.ps1 | iex"
    ;;
  unknown)
    error "Unsupported OS: $(uname -s). kari ships binaries for linux, darwin (macOS), windows, and android (Termux)."
    ;;
esac

if [ "$ARCH" = "unknown" ]; then
  error "Unsupported architecture: $(uname -m). kari ships amd64 and arm64 builds only."
fi

if [ "$OS" = "android" ] && [ "$ARCH" != "arm64" ]; then
  error "kari only publishes an android-arm64 build; detected arch $(uname -m) is not supported on Termux."
fi

ASSET="kari-${OS}-${ARCH}"

# --- 2. Determine install dir (override with KARI_INSTALL_DIR) ---
if [ "$OS" = "android" ]; then
  DEFAULT_INSTALL_DIR="${PREFIX:-/data/data/com.termux/files/usr}/bin"
else
  DEFAULT_INSTALL_DIR="$HOME/.local/bin"
fi
INSTALL_DIR="${KARI_INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"

mkdir -p "$INSTALL_DIR"

# --- 3. Download (atomic install) ---
DEST="$INSTALL_DIR/kari"
TMP="$DEST.tmp.$$"
DOWNLOAD_URL="${BASE_URL}/${ASSET}"

cleanup() { rm -f "$TMP"; }
trap cleanup EXIT

info "Downloading kari (${OS}/${ARCH})..."
info "  ${DOWNLOAD_URL}"

if ! curl -fsSL -o "$TMP" "$DOWNLOAD_URL"; then
  error "Download failed (asset '${ASSET}' may not exist in the latest release, or you're offline).
  URL: ${DOWNLOAD_URL}"
fi

chmod +x "$TMP"
mv -f "$TMP" "$DEST"
trap - EXIT

# macOS tags files downloaded by quarantine-aware apps (Safari, Chrome, some
# EDR/MDM tools) with com.apple.quarantine, which makes Gatekeeper refuse to
# run the binary ("cannot be opened because the developer cannot be
# verified"). Plain curl doesn't set this itself, but strip it defensively in
# case something in the chain does -- it's a no-op (fails silently) when the
# attribute isn't present.
if [ "$OS" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$DEST" 2>/dev/null || true
fi

info "Installed kari to ${DEST}"

# --- 4. PATH handling ---
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
  # $SHELL is the user's login shell, not necessarily whatever is running this
  # script (curl | bash always runs bash regardless of login shell) -- that's
  # intentional, we want to edit the rc file the user's actual shell reads.
  SHELL_NAME="$(basename "${SHELL:-sh}")"

  case "$SHELL_NAME" in
    fish)
      RC="$HOME/.config/fish/config.fish"
      LINE="fish_add_path $INSTALL_DIR"
      ;;
    zsh)
      RC="${ZDOTDIR:-$HOME}/.zshrc"
      LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
      ;;
    bash)
      if [ "$OS" = "darwin" ]; then
        # macOS Terminal.app launches login shells; ~/.bashrc isn't sourced by default.
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

  if [ -w "$RC" ] && grep -qxF "$LINE" "$RC" 2>/dev/null; then
    ALREADY_IN_RC=1
  elif [ -w "$RC" ]; then
    printf '\n# Added by kari installer\n%s\n' "$LINE" >> "$RC"
    PATH_UPDATED=1
  fi
fi

# --- 5. Verify & report ---
VERSION_OUTPUT=""
if [ -x "$DEST" ]; then
  VERSION_OUTPUT="$("$DEST" -v 2>/dev/null || true)"
fi

info ""
info "kari installed successfully!"
[ -n "$VERSION_OUTPUT" ] && info "Version:  $VERSION_OUTPUT"
info "Location: $DEST"

if [ "$ALREADY_ON_PATH" -eq 1 ]; then
  info "Run 'kari' to get started."
elif [ "$PATH_UPDATED" -eq 1 ]; then
  info ""
  info "Added ${INSTALL_DIR} to PATH in ${RC}."
  info "Restart your terminal, or run:"
  info "  source ${RC}"
  info "to use the 'kari' command right away."
elif [ "$ALREADY_IN_RC" -eq 1 ]; then
  info ""
  info "${INSTALL_DIR} is already configured in ${RC} from a previous install."
  info "Restart your terminal (or 'source ${RC}') to pick it up."
else
  info ""
  info "Note: ${INSTALL_DIR} is not on your PATH and I couldn't update a shell config for it."
  info "Add it manually, e.g.: export PATH=\"${INSTALL_DIR}:\$PATH\""
fi
