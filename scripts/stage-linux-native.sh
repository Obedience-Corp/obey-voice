#!/usr/bin/env bash
# Shared by development installation and release packaging. No compilation or Python.
set -euo pipefail
[[ $# == 2 ]] || { echo "usage: stage-linux-native.sh BINARY STAGE" >&2; exit 2; }
source_binary="$1"
stage="$2"
bin="$stage/obey-voice"
libdir="$stage/lib"
licensedir="$stage/licenses"
mkdir -p "$libdir" "$licensedir"
if [[ "$source_binary" != "$bin" ]]; then cp "$source_binary" "$bin"; fi

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

