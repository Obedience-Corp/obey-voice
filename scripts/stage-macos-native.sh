#!/usr/bin/env bash
# Shared by development installation and release packaging. No compilation or Python.
set -euo pipefail
[[ $# == 2 ]] || { echo "usage: stage-macos-native.sh BINARY STAGE" >&2; exit 2; }
source_binary="$1"
stage="$2"
bin="$stage/obey-voice"
libdir="$stage/lib"
licensedir="$stage/licenses"
mkdir -p "$libdir" "$licensedir"
if [[ "$source_binary" != "$bin" ]]; then cp "$source_binary" "$bin"; fi

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

