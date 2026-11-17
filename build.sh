#!/usr/bin/env bash
set -euo pipefail

APP_NAME="kari"
BUILD_DIR="kari-build"
PKG="./cmd/kari"

DESCRIBE=$(git describe --tags --match='v*' 2>/dev/null || true)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "dev")

if [ -z "$DESCRIBE" ]; then
  COUNT=$(git rev-list --count HEAD 2>/dev/null || echo "0")
  VERSION="v1.0.${COUNT}"
else
  D="${DESCRIBE#v}"
  case "$D" in
    *-*-g*)
      IFS='.-' read -r MAJOR MINOR PATCH COMMITS _ <<< "$D"
      VERSION="v${MAJOR}.${MINOR}.$((PATCH + COMMITS))"
      ;;
    *)
      VERSION="v${D}"
      ;;
  esac
fi
LDFLAGS="-s -w -X kari/internal/app.Version=$VERSION -X kari/internal/app.Commit=$COMMIT"

# Version and commit are already supplied above; avoid Go's extra VCS queries.
BASE_FLAGS=("-buildvcs=false" "-ldflags=$LDFLAGS")
# Plain ./build.sh shares package-cache entries with ordinary Go commands.
# Release/cross-platform builds still omit local paths for reproducibility.
if [ "$#" -gt 0 ]; then
  BASE_FLAGS=("-trimpath" "${BASE_FLAGS[@]}")
fi

get_host_os() {
  if [ -n "${TERMUX_VERSION:-}" ]; then
    echo "android"
    return
  fi
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    CYGWIN*|MINGW*|MSYS*) echo "windows" ;;
    *)      echo "darwin" ;;
  esac
}

get_host_arch() {
  case "$(uname -m)" in
    x86_64)    echo "amd64" ;;
    arm64)     echo "arm64" ;;
    aarch64)  echo "arm64" ;;
    *)        echo "arm64" ;;
  esac
}

binary_name() {
  local goos="$1" goarch="$2"
  local name="$APP_NAME-$goos-$goarch"
  if [ "$goos" = "windows" ]; then
    name+=".exe"
  fi
  echo "$name"
}

build_one() {
  local goos="$1" goarch="$2" out="$3"
  local args=("${BASE_FLAGS[@]}")
  # Android requires PIE for modern versions.
  if [ "$goos" = "android" ]; then
    args+=("-buildmode=pie")
  fi
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build "${args[@]}" -o "$out" "$PKG"
}

if [ "${1:-}" = "all" ]; then
  mkdir -p "$BUILD_DIR"

  platforms=(
    "linux/amd64"
    "linux/arm64"
    "windows/amd64"
    "windows/arm64"
    "darwin/amd64"
    "darwin/arm64"
    "android/arm64"
  )

  echo "Building all platforms..."

  # Keep a bounded number of builds in flight so parallel go builds do not
  # oversubscribe the CPU. PIDs are reaped FIFO, which keeps the window full
  # without relying on `wait -n` (missing from the bash 3.2 shipped by macOS).
  MAX_JOBS="${MAX_JOBS:-4}"
  case "$MAX_JOBS" in
    '' | *[!0-9]*)
      echo "MAX_JOBS must be a positive integer, got '$MAX_JOBS'" >&2
      exit 2
      ;;
  esac
  if [ "$MAX_JOBS" -lt 1 ]; then
    echo "MAX_JOBS must be at least 1, got '$MAX_JOBS'" >&2
    exit 2
  fi

  pids=()
  outputs=()
  failed=0

  for platform in "${platforms[@]}"; do
    IFS="/" read -r GOOS GOARCH <<< "$platform"
    output_name=$(binary_name "$GOOS" "$GOARCH")
    outputs+=("$BUILD_DIR/$output_name")

    echo "Building $output_name..."
    (
      build_one "$GOOS" "$GOARCH" "$BUILD_DIR/$output_name" || {
        echo "FAILED: $output_name" >&2
        exit 1
      }
    ) &
    pids+=("$!")

    if [ "${#pids[@]}" -ge "$MAX_JOBS" ]; then
      wait "${pids[0]}" || failed=$((failed + 1))
      pids=("${pids[@]:1}")
    fi
  done

  # A cap at least as wide as the platform list leaves nothing queued here,
  # and bash 3.2 (the macOS /bin/bash) errors on "${pids[@]}" under `set -u`
  # when the array is empty.
  if [ "${#pids[@]}" -gt 0 ]; then
    for pid in "${pids[@]}"; do
      wait "$pid" || failed=$((failed + 1))
    done
  fi

  if [ "$failed" -gt 0 ]; then
    echo "$failed build(s) failed." >&2
    exit 1
  fi

  # Compress with UPX when available to make the updater download smaller.
  # Run the compressions in parallel: -9 takes several seconds per binary,
  # so doing them one after another dominates the whole `all` build.
  # Set UPX=0 to skip compression entirely. Only the binaries built above are
  # passed to upx so stale files in the build directory are left alone.
  if [ "${UPX:-1}" != "0" ] && command -v upx >/dev/null 2>&1; then
    echo "Compressing binaries with UPX..."
    for f in "${outputs[@]}"; do
      upx -9 "$f" >/dev/null 2>&1 &
    done
    wait || true
  fi

  echo "All builds complete."
elif [ "${1:-}" = "target" ]; then
  GOOS="${2:-}"
  GOARCH="${3:-}"
  if [ -z "$GOOS" ] || [ -z "$GOARCH" ]; then
    echo "usage: $0 target <os> <arch>" >&2
    exit 1
  fi

  output_name=$(binary_name "$GOOS" "$GOARCH")
  echo "Building $output_name for ${GOOS}/${GOARCH}..."
  build_one "$GOOS" "$GOARCH" "$output_name"
  echo "Done."
else
  GOOS=$(get_host_os)
  GOARCH=$(get_host_arch)

  output_name="$APP_NAME"
  if [ "$GOOS" = "windows" ]; then
    output_name+=".exe"
  fi

  echo "Building $output_name for ${GOOS}/${GOARCH}..."
  build_one "$GOOS" "$GOARCH" "$output_name"
  echo "Done."
fi
