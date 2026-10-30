#!/usr/bin/env bash
# Public installer + actual SDK/daemon + Voice adapter, disposable non-root scratch.
set -euo pipefail
cd "$(dirname "$0")/../.."
expected=${OBEY_EXPECT_VERSION:-}
if [ -z "$expected" ]; then
 expected=$(gh api repos/Obedience-Corp/obey-releases/releases/latest --jq .tag_name)
 expected=${expected#v}
fi
[[ "$expected" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Expected a stable Obey version' >&2; exit 2; }
[ "$(id -u)" != 0 ] || { echo 'Run as a normal user' >&2; exit 2; }
image="voice-obey-acceptance:$(id -u)-$(id -g)"
docker build --build-arg "FIXTURE_UID=$(id -u)" --build-arg "FIXTURE_GID=$(id -g)" -t "$image" tests/obeydaemon
container=$(docker create --init -e "OBEY_EXPECT_VERSION=$expected" -e "FESTIVAL_VERSION=${FESTIVAL_VERSION:-0.3.26}" \
 --mount "type=bind,src=$PWD,dst=/voice,readonly" "$image" sleep infinity)
cleanup() { docker rm -f "$container" >/dev/null; }
trap cleanup EXIT
docker start "$container" >/dev/null
status=0
docker exec "$container" timeout --signal=TERM --kill-after=10s 600 bash /voice/tests/obeydaemon/verify.sh || status=$?
out=${VOICE_OBEY_EVIDENCE:-$PWD/.test-output/obey-daemon}
mkdir -p "$out"
# Stream from owned scratch; Docker's archive API may miss tmpfs-mounted files.
docker exec "$container" tar -C /fixture/evidence -cf - . | tar -C "$out" -xf -
exit "$status"
