#!/usr/bin/env bash
# build-webrtc-apm.sh — vendor-build libwebrtc-audio-processing (AEC3) for the
# `apm` cgo shim (pkg/voiceagent/audio/apm, cmd/apmharness).
#
# The library is NOT committed to this repo (third_party/ is gitignored): this
# script clones the freedesktop.org source at a pinned commit and meson-builds
# it into a local prefix, mirroring how sherpa-onnx is vendored via go.mod
# rather than by checking in its source.
#
# ---------------------------------------------------------------- behavior
#
# 1. If a system `webrtc-audio-processing-1` pkg-config file is already
#    resolvable (distro package on Linux — Debian/Ubuntu ship
#    libwebrtc-audio-processing-dev, Fedora webrtc-audio-processing-devel,
#    Arch webrtc-audio-processing-1), this script does nothing: cgo picks it
#    up via ordinary system pkg-config, no local build needed.
# 2. Otherwise (macOS has no Homebrew formula for this library) it clones the
#    source into third_party/webrtc-audio-processing and meson-builds it into
#    third_party/webrtc-apm-prefix. Already-built output is left in place on
#    re-run; pass --clean to force a rebuild.
#
# ---------------------------------------------------------------- prereqs
#
# meson, ninja, pkg-config. On macOS, installed via `brew install` if missing
# (no other packages are touched). On Linux, install through your distro's
# package manager if they are not already present.
#
# ---------------------------------------------------------------- usage
#
#   scripts/build-webrtc-apm.sh
#   scripts/build-webrtc-apm.sh --clean   # wipe and rebuild the local prefix
#
# On success when a local build ran, export the printed PKG_CONFIG_PATH before
# building the `apm`-tagged Go packages:
#
#   eval "$(scripts/build-webrtc-apm.sh --print-env)"
#   go build -tags apm ./...
set -euo pipefail

# Pinned at the v1.3 tag (the same release the GPD Linux gate has installed
# from its distro package), so the macOS build and the Linux system package
# expose byte-identical headers/ABI shape.
WEBRTC_APM_REF="${WEBRTC_APM_REF:-8e258a1933d405073c9e6465628a69ac7d2a1f13}"
WEBRTC_APM_REPO="${WEBRTC_APM_REPO:-https://gitlab.freedesktop.org/pulseaudio/webrtc-audio-processing.git}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_DIR="$ROOT_DIR/third_party/webrtc-audio-processing"
BUILD_DIR="$SRC_DIR/build"
PREFIX_DIR="$ROOT_DIR/third_party/webrtc-apm-prefix"
PKGCONFIG_DIR="$PREFIX_DIR/lib/pkgconfig"

clean=false
print_env_only=false
for arg in "$@"; do
  case "$arg" in
    --clean) clean=true ;;
    --print-env) print_env_only=true ;;
    *)
      echo "build-webrtc-apm.sh: unknown argument: $arg" >&2
      exit 1
      ;;
  esac
done

log() { echo "build-webrtc-apm: $*" >&2; }

if $print_env_only; then
  if [ -f "$PKGCONFIG_DIR/webrtc-audio-processing-1.pc" ]; then
    echo "export PKG_CONFIG_PATH=\"$PKGCONFIG_DIR:\${PKG_CONFIG_PATH:-}\""
  fi
  exit 0
fi

if $clean; then
  log "removing $BUILD_DIR and $PREFIX_DIR"
  rm -rf "$BUILD_DIR" "$PREFIX_DIR"
fi

# 1. A system package (Linux distro, or a previously exported local prefix)
# already satisfies the dependency — nothing to build.
if pkg-config --exists webrtc-audio-processing-1 2>/dev/null; then
  found_version="$(pkg-config --modversion webrtc-audio-processing-1)"
  log "system webrtc-audio-processing-1 $found_version already resolvable via pkg-config; nothing to build"
  pkg-config --cflags webrtc-audio-processing-1 >&2
  exit 0
fi

log "no system webrtc-audio-processing-1 found; vendor-building into $PREFIX_DIR"

# 2. Toolchain: meson/ninja/pkg-config only, per the hard rule against
# installing anything else system-wide.
os="$(uname -s)"
missing=()
for tool in meson ninja pkg-config; do
  command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
if [ "${#missing[@]}" -gt 0 ]; then
  if [ "$os" = "Darwin" ] && command -v brew >/dev/null 2>&1; then
    log "installing missing build tools via brew: ${missing[*]}"
    brew install "${missing[@]}"
  else
    log "missing required tools: ${missing[*]} (install them with your package manager)"
    exit 1
  fi
fi

# 3. Clone (or reuse) the pinned source.
if [ ! -d "$SRC_DIR/.git" ]; then
  log "cloning $WEBRTC_APM_REPO @ $WEBRTC_APM_REF into $SRC_DIR"
  mkdir -p "$(dirname "$SRC_DIR")"
  git clone "$WEBRTC_APM_REPO" "$SRC_DIR"
  git -C "$SRC_DIR" checkout --detach "$WEBRTC_APM_REF"
else
  current="$(git -C "$SRC_DIR" rev-parse HEAD)"
  if [ "$current" != "$WEBRTC_APM_REF" ]; then
    log "warning: $SRC_DIR is checked out at $current, not the pinned $WEBRTC_APM_REF (leaving as-is; pass --clean to reset)"
  fi
fi

# 4. Meson build. wrap_mode stays at its default (meson fetches the abseil-cpp
# subproject automatically if no system absl pkg-config files are found) — no
# extra flag needed, and no network access happens if system absl is present.
if [ ! -f "$PKGCONFIG_DIR/webrtc-audio-processing-1.pc" ]; then
  log "meson setup ($BUILD_DIR -> $PREFIX_DIR)"
  meson setup "$BUILD_DIR" "$SRC_DIR" \
    --prefix="$PREFIX_DIR" \
    --buildtype=release \
    --default-library=static \
    -Db_staticpic=true
  log "meson compile"
  meson compile -C "$BUILD_DIR"
  log "meson install"
  meson install -C "$BUILD_DIR"
else
  log "already built at $PREFIX_DIR (pass --clean to rebuild)"
fi

log "done. Before building apm-tagged Go code, run:"
log "  eval \"\$(scripts/build-webrtc-apm.sh --print-env)\""
