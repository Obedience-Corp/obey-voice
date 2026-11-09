#!/usr/bin/env bash
# CI bootstrap: use the asset API instead of the intermittently failing public
# release-download route. Pins are from casey/just 1.59.0 release SHA-256 digests.
set -euo pipefail
: "${RUNNER_TEMP:?CI runner scratch directory required}"
: "${GITHUB_PATH:?CI path file required}"
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)
    asset=642949555
    checksum=409044617b6f06517c1035e55ca38c8c856ff8b650e41bc2aa1ef82d5903a23e
    ;;
  Darwin/arm64)
    asset=643129724
    checksum=6574cea774ed4cae12e24b6aa11857de33e80dab4667cf628fd5840df96a2a9d
    ;;
  Darwin/x86_64)
    asset=643131819
    checksum=036ead9df9bc31dbae9934ae3b4236ed124d0bafcffd33439460ae084d5c9cb7
    ;;
  *) echo 'Unsupported CI just platform' >&2; exit 1 ;;
esac
scratch=$(mktemp -d "$RUNNER_TEMP/just-download.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
for attempt in 1 2 3; do
  if gh api "repos/casey/just/releases/assets/$asset" \
    -H 'Accept: application/octet-stream' > "$scratch/just.tar.gz"; then
    break
  fi
  if [ "$attempt" = 3 ]; then exit 1; fi
  sleep 2
done
printf '%s  %s\n' "$checksum" "$scratch/just.tar.gz" | shasum -a 256 -c -
tar -xzf "$scratch/just.tar.gz" -C "$scratch" just
install_dir="$RUNNER_TEMP/just-1.59.0/bin"
mkdir -p "$install_dir"
install -m 755 "$scratch/just" "$install_dir/just"
"$install_dir/just" --version
printf '%s\n' "$install_dir" >> "$GITHUB_PATH"
