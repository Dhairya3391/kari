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
      IFS='.-' read -r MAJOR MINOR PATCH COMMITS HASH <<< "$D"
      VERSION="v${MAJOR}.${MINOR}.$((PATCH + COMMITS))"
      ;;
    *)
      VERSION="v${D}"
      ;;
  esac
fi
LDFLAGS="-s -w -X kari/internal/app.Version=$VERSION -X kari/internal/app.Commit=$COMMIT"

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

  # Use a simple worker pool to avoid overloading the system
  # Max 4 parallel builds
  MAX_JOBS=4
  job_count=0
  failed=0

  for platform in "${platforms[@]}"; do
    IFS="/" read -r GOOS GOARCH <<< "$platform"

    output_name="$APP_NAME-$GOOS-$GOARCH"
    if [ "$GOOS" = "windows" ]; then
      output_name+=".exe"
    fi

    # Build flags as an array to handle quoting correctly
    build_args=("-trimpath" "-ldflags=$LDFLAGS")

    # Android requires PIE for modern versions
    if [ "$GOOS" = "android" ]; then
      build_args+=("-buildmode=pie")
    fi

    echo "Building $output_name..."
    (
      CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build "${build_args[@]}" \
      -o "$BUILD_DIR/$output_name" "$PKG" 2>&1 || {
        echo "FAILED: $output_name" >&2
        exit 1
      }
    ) &

    ((job_count++))
    if [ "$job_count" -ge "$MAX_JOBS" ]; then
      wait -n || ((failed++))
      ((job_count--))
    fi
  done

  # Wait for remaining jobs
  while [ "$job_count" -gt 0 ]; do
    wait -n || ((failed++))
    ((job_count--))
  done

  if [ "$failed" -gt 0 ]; then
    echo "$failed build(s) failed."
    exit 1
  fi

  # Optional: Compress with UPX if available to make updater faster
  if command -v upx >/dev/null 2>&1; then
    echo "Compressing binaries with UPX..."
    # Only compress if there are files
    if ls "$BUILD_DIR"/* >/dev/null 2>&1; then
      upx -9 "$BUILD_DIR"/* >/dev/null 2>&1 || true
    fi
  fi

  echo "All builds complete."
elif [ "${1:-}" = "target" ]; then
  GOOS=$2
  GOARCH=$3

  output_name="$APP_NAME-$GOOS-$GOARCH"
  if [ "$GOOS" = "windows" ]; then
    output_name+=".exe"
  fi

  build_args=("-trimpath" "-ldflags=$LDFLAGS")
  if [ "$GOOS" = "android" ]; then
    build_args+=("-buildmode=pie")
  fi

  echo "Building $output_name for ${GOOS}/${GOARCH}..."

  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build "${build_args[@]}" \
  -o "$output_name" "$PKG"

  echo "Done."
else
  GOOS=$(get_host_os)
  GOARCH=$(get_host_arch)

  output_name="$APP_NAME"
  if [ "$GOOS" = "windows" ]; then
    output_name+=".exe"
  fi

  build_args=("-trimpath" "-ldflags=$LDFLAGS")
  if [ "$GOOS" = "android" ]; then
    build_args+=("-buildmode=pie")
  fi

  echo "Building $output_name for ${GOOS}/${GOARCH}..."

  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build "${build_args[@]}" \
  -o "$output_name" "$PKG"

  echo "Done."
  fi
