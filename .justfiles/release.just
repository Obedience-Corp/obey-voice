# Release tasks for Obey Voice.
#
# Native CGO archives are shared by every distribution channel. Publication
# stages both supported platforms in a draft and validates remote readback.

[private]
default:
    @just --list --justfile {{source_file()}}

# Create/resume a release and verify every enabled package channel.
[no-cd]
[positional-arguments]
create version:
    python3 scripts/release_pipeline.py "$1"

# Read-only release plan: versions, channels, permissions and blockers.
[no-cd]
[positional-arguments]
plan version:
    python3 scripts/release_pipeline.py "$1" --plan

# Validate tracked media coverage and byte-bound review records (not clearance).
[no-cd]
media-check:
    python3 scripts/media_review.py

# Fail until every inventoried asset has a complete public-review record.
[no-cd]
media-ready:
    python3 scripts/media_review.py --require-cleared

# Isolated negative paths for media inventory and clearance checks.
[no-cd]
media-test:
    python3 -m unittest discover -s scripts -p test_media_review.py

# Native-only tag/push after tests. Prefer create for coordinated channel releases.
[no-cd]
tag version:
    #!/usr/bin/env bash
    set -euo pipefail
    [ -z "$(git status --porcelain)" ] || { echo "working tree is dirty — commit or stash first" >&2; exit 1; }
    echo "Running tests…"
    go test ./internal/... ./cmd/...
    git tag -a "{{version}}" -m "Obey Voice {{version}}"
    git push origin "{{version}}"
    echo
    echo "Tagged {{version}}. Next:"
    echo "  Release Artifacts will validate both platforms, stage a draft, then publish."
    echo "  Inspect the workflow before promoting any package channel."

# Publish a pushed tag using both versioned archives + sidecars in dist/release.
[no-cd]
publish version:
    python3 scripts/publish_release.py "{{version}}"

# Packaging integrity and interrupted-publication regression tests (no network).
[no-cd]
artifact-test:
    python3 -m unittest discover -s scripts -p test_release_*.py
    python3 -m unittest discover -s scripts -p test_desktop_*.py

# Build a signed/notarized desktop archive from a clean release tag.
[no-cd]
[positional-arguments]
desktop-artifact version:
    python3 scripts/desktop_artifacts.py "$1"

# Local packaging preview only; cannot be published by the release coordinator.
[no-cd]
[positional-arguments]
desktop-preview version="v0.0.0":
    python3 scripts/desktop_artifacts.py "$1" --unsigned

# Render the org tap formula from both verified archives in dist/release.
[no-cd]
homebrew-formula version:
    python3 scripts/render_homebrew.py "{{version}}"

# Validate, bottle, upgrade and uninstall in a new isolated Homebrew prefix.
[no-cd]
homebrew-check version workspace:
    python3 scripts/verify_homebrew.py "{{version}}" --workspace "{{workspace}}"

# Reject incomplete or corrupted formula inputs before changing generated output.
[no-cd]
homebrew-test:
    python3 -m unittest discover -s scripts -p test_homebrew.py

# Produces dist/obey-voice-linux-amd64.tar.gz: the native CGO binary plus the
# sherpa-onnx/onnxruntime shared libraries it needs. patchelf replaces the
# module-cache RUNPATH injected by sherpa's cgo flags with $ORIGIN/lib, then an
# extraction smoke test proves the tarball runs without the builder's module
# cache or LD_LIBRARY_PATH.
#
# Build a relocatable, self-contained glibc Linux amd64 release artifact
[no-cd]
linux-artifact:
    #!/usr/bin/env bash
    set -euo pipefail

    if [[ "$(uname -s)" != "Linux" || "$(uname -m)" != "x86_64" ]]; then
        echo "linux-artifact builds a native amd64 artifact and must run on x86_64 Linux" >&2
        exit 1
    fi
    for tool in curl patchelf readelf ldd sha256sum strings; do
        command -v "$tool" >/dev/null || {
            echo "linux-artifact requires $tool (Arch: sudo pacman -S curl patchelf binutils; Debian/Ubuntu: sudo apt install curl patchelf binutils)" >&2
            exit 1
        }
    done

    version="$(python3 scripts/release_artifacts.py version)"
    platform="linux-amd64"
    stage="dist/obey-voice-${platform}"
    tarball="dist/obey-voice-${platform}.tar.gz"
    bin="$stage/obey-voice"
    libdir="$stage/lib"
    licensedir="$stage/licenses"

    rm -rf "$stage" "$tarball"
    mkdir -p "$libdir" "$licensedir"

    echo "Building native CGO amd64 binary…"
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
        go build -buildvcs=false -trimpath \
        -ldflags "-X github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd.releaseVersion=$version" \
        -o "$bin" ./cmd/obey-voice

    bash scripts/stage-linux-native.sh "$bin" "$stage"

    python3 scripts/release_artifacts.py package "$stage" "$platform" "$version"
    python3 scripts/release_artifacts.py smoke "dist/release/obey-voice-${version}-${platform}.tar.gz" "$platform" "$version"

    smoke_root="$(mktemp -d)"
    trap 'rm -rf "$smoke_root"' EXIT
    tar -xzf "$tarball" -C "$smoke_root"
    smoke_bin="$smoke_root/obey-voice-${platform}/obey-voice"
    smoke_lib="$smoke_root/obey-voice-${platform}/lib"
    env -u LD_LIBRARY_PATH "$smoke_bin" --version
    smoke_ldd="$(env -u LD_LIBRARY_PATH ldd "$smoke_bin")"
    for lib in libsherpa-onnx-c-api.so libonnxruntime.so; do
        grep -F "$smoke_lib/$lib" <<<"$smoke_ldd" >/dev/null || {
            echo "relocation smoke test did not load bundled $lib:" >&2
            echo "$smoke_ldd" >&2
            exit 1
        }
    done

    echo
    echo "Artifact: $tarball"
    sha256sum "$tarball"
    echo "Contents:"
    tar -tzf "$tarball"

# Produces dist/obey-voice-macos-arm64.tar.gz: the CGO-built binary plus every
# non-system sherpa-onnx/onnxruntime dylib it needs, laid out so the binary
# finds them via @executable_path/lib from any directory, with a manifest.json
# (git-describe version + per-file sha256). The Obey Voice Mac app vendors this
# artifact; signing happens in the app's release pipeline (delta D5,
# workflow/design/obey-voice-apps/PROTOCOL_DELTAS.md).
#
# License packaging rejects missing notices, new libraries and version/digest drift.
[no-cd]
macos-notices-test:
    python3 -m unittest discover -s scripts -p test_package_macos_notices.py

# Build a relocatable, self-contained macOS arm64 release artifact
[no-cd]
macos-artifact:
    #!/usr/bin/env bash
    set -euo pipefail

    if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
        echo "macos-artifact builds a native arm64 artifact and must run on Apple Silicon macOS" >&2
        exit 1
    fi

    version="$(python3 scripts/release_artifacts.py version)"
    platform="macos-arm64"
    stage="dist/obey-voice-${platform}"
    tarball="dist/obey-voice-${platform}.tar.gz"
    bin="$stage/obey-voice"
    libdir="$stage/lib"

    rm -rf "$stage" "$tarball"
    mkdir -p "$libdir"

    echo "Building CGO arm64 binary…"
    CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
        go build -buildvcs=false -trimpath \
        -ldflags "-X github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd.releaseVersion=$version" \
        -o "$bin" ./cmd/obey-voice

    bash scripts/stage-macos-native.sh "$bin" "$stage"

    python3 scripts/release_artifacts.py package "$stage" "$platform" "$version"
    python3 scripts/release_artifacts.py smoke "dist/release/obey-voice-${version}-${platform}.tar.gz" "$platform" "$version"
