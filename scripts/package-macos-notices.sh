#!/usr/bin/env bash
# Run from the repo root after native dylib collection, before hashing the archive.
set -euo pipefail

if [[ $# != 2 ]]; then
    echo "usage: package-macos-notices.sh STAGE SHERPA_MACOS_MODULE_DIR" >&2
    exit 2
fi
stage="$1"
module_dir="$2"
licensedir="$stage/licenses"

# Adding another native dependency requires its distribution notices too.
for library in "$stage"/lib/*.dylib; do
    case "$(basename "$library")" in
        libsherpa-onnx-c-api.dylib|libsherpa-onnx-cxx-api.dylib|libonnxruntime.dylib) ;;
        *) echo "unreviewed native dependency for license packaging: $library" >&2; exit 1 ;;
    esac
done

# Same verified upstream material as linux-artifact. Do not carry it forward
# silently if a future sherpa module bundles a different ONNX Runtime version.
onnxruntime_version="1.28.2"
strings "$stage/lib/libonnxruntime.dylib" | grep -Fx "$onnxruntime_version" >/dev/null || {
    echo "bundled ONNX Runtime version no longer matches license version $onnxruntime_version" >&2
    exit 1
}

mkdir -p "$licensedir"
cp LICENSE "$stage/LICENSE"
cp NOTICE "$stage/NOTICE"
cp "$module_dir/LICENSE" "$licensedir/sherpa-onnx-go-macos-LICENSE"
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
        shasum -a 256 --check -
)
