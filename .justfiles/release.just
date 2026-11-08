# Release tasks for Obey Voice.
#
# Native CGO archives are shared by every distribution channel. Publication
# stages both supported platforms in a draft and validates remote readback.

[private]
default:
    @just --list --justfile {{source_file()}}

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

# Tag and push a release after tests pass. Usage: just release tag v0.1.0
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

    module_dir="$(
        go list -m -json github.com/k2-fsa/sherpa-onnx-go-linux |
            awk -F'"' '$2 == "Dir" { print $4; exit }'
    )"
    [[ -n "$module_dir" ]] || {
        echo "cannot locate github.com/k2-fsa/sherpa-onnx-go-linux module directory" >&2
        exit 1
    }
    native_dir="$module_dir/lib/x86_64-unknown-linux-gnu"
    for lib in libsherpa-onnx-c-api.so libonnxruntime.so; do
        [[ -f "$native_dir/$lib" ]] || {
            echo "required native library is missing: $native_dir/$lib" >&2
            exit 1
        }
        cp "$native_dir/$lib" "$libdir/$lib"
    done
    cp LICENSE "$stage/LICENSE"
    cp NOTICE "$stage/NOTICE"
    cp "$module_dir/LICENSE" "$licensedir/sherpa-onnx-go-linux-LICENSE"

    # sherpa-onnx-go-linux v1.13.8 bundles ONNX Runtime 1.28.2 without its
    # redistribution files. Pin and verify the upstream license material, and
    # fail loudly if a future module update changes the bundled runtime.
    onnxruntime_version="1.28.2"
    strings "$native_dir/libonnxruntime.so" | grep -Fx "$onnxruntime_version" >/dev/null || {
        echo "bundled ONNX Runtime version no longer matches license version $onnxruntime_version" >&2
        exit 1
    }
    onnxruntime_base="https://raw.githubusercontent.com/microsoft/onnxruntime/v${onnxruntime_version}"
    curl -fsSL "$onnxruntime_base/LICENSE" -o "$licensedir/onnxruntime-LICENSE"
    curl -fsSL "$onnxruntime_base/ThirdPartyNotices.txt" -o "$licensedir/onnxruntime-ThirdPartyNotices.txt"
    (
        cd "$licensedir"
        printf '%s  %s\n' \
            "2f07c72751aed99790b8a4869cf2311df85a860b22ded05fa22803587a48922c" \
            "onnxruntime-LICENSE" \
            "0e07b95f3a8d6230037707c5c4a2b554d12c4cb67369669ac255635528ffcee2" \
            "onnxruntime-ThirdPartyNotices.txt" |
            sha256sum --check -
    )

    patchelf --set-rpath '$ORIGIN/lib' "$bin"
    runpath="$(patchelf --print-rpath "$bin")"
    [[ "$runpath" == '$ORIGIN/lib' ]] || {
        echo "unexpected Linux artifact RUNPATH: $runpath" >&2
        exit 1
    }
    if readelf -d "$bin" | grep -F "$module_dir"; then
        echo "Linux artifact still references the builder module cache" >&2
        exit 1
    fi
    if ldd "$bin" | grep -q 'not found'; then
        echo "Linux artifact has unresolved shared libraries:" >&2
        ldd "$bin" >&2
        exit 1
    fi

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

    # The cgo LDFLAGS bake the sherpa-onnx module-cache dir in as an LC_RPATH;
    # that is where @rpath dylibs are found before relocation.
    mapfile -t rpaths < <(otool -l "$bin" | awk '$1=="cmd"&&$2=="LC_RPATH"{g=1} g&&$1=="path"{print $2; g=0}')

    locate_dep() {
        local base="$1" rp
        for rp in "${rpaths[@]}"; do
            [[ "$rp" == @* ]] && continue
            [[ -f "$rp/$base" ]] && { echo "$rp/$base"; return 0; }
        done
        return 1
    }

    # Recursively copy every non-system (@rpath) dylib the binary and its
    # dylibs reference (otool -L), resolving each from the baked-in rpaths.
    declare -A copied
    worklist=("$bin")
    while ((${#worklist[@]})); do
        cur="${worklist[0]}"; worklist=("${worklist[@]:1}")
        while read -r dep; do
            [[ "$dep" == @rpath/* ]] || continue
            base="${dep#@rpath/}"
            [[ -n "${copied[$base]:-}" ]] && continue
            src="$(locate_dep "$base")" || { echo "cannot locate dylib $base in build rpaths" >&2; exit 1; }
            cp "$src" "$libdir/$base"
            chmod u+w "$libdir/$base"
            copied[$base]=1
            worklist+=("$libdir/$base")
        done < <(otool -L "$cur" | tail -n +2 | awk '{print $1}')
    done

    (( ${#copied[@]} )) || { echo "no bundled dylibs discovered — expected sherpa-onnx/onnxruntime" >&2; exit 1; }
    echo "Bundled ${#copied[@]} dylib(s): ${!copied[*]}"

    # Relocate: the binary finds its libs next to it; drop absolute build rpaths.
    install_name_tool -add_rpath "@executable_path/lib" "$bin"
    for rp in "${rpaths[@]}"; do
        [[ "$rp" == @* ]] && continue
        install_name_tool -delete_rpath "$rp" "$bin" 2>/dev/null || true
    done
    # Each dylib resolves its siblings from its own directory.
    for base in "${!copied[@]}"; do
        install_name_tool -add_rpath "@loader_path" "$libdir/$base" 2>/dev/null || true
    done

    # install_name_tool invalidates the signature; arm64 needs a valid one to
    # load. Ad-hoc re-sign here; the app pipeline re-signs with a real identity.
    for base in "${!copied[@]}"; do
        codesign --force --sign - "$libdir/$base"
    done
    codesign --force --sign - "$bin"

    module_dir="$(
        go list -m -json github.com/k2-fsa/sherpa-onnx-go-macos |
            awk -F'"' '$2 == "Dir" { print $4; exit }'
    )"
    [[ -n "$module_dir" ]] || { echo "cannot locate sherpa-onnx-go-macos module directory" >&2; exit 1; }
    bash scripts/package-macos-notices.sh "$stage" "$module_dir"

    python3 scripts/release_artifacts.py package "$stage" "$platform" "$version"
    python3 scripts/release_artifacts.py smoke "dist/release/obey-voice-${version}-${platform}.tar.gz" "$platform" "$version"
