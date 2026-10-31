#!/usr/bin/env bash
# Capture the config surface's --json payloads from the real binary.
#
# These fixtures are the wire contract between obey-voice and the Obey Voice Mac
# app: the app's decoding tests read copies of them. They are captured, never
# hand-written — a hand-written fixture proves the app can decode a payload
# obey-voice does not actually emit.
#
# Usage: cmd/obey-voice/cmd/testdata/capture-config-fixtures.sh
#
# The install root is a throwaway HOME so the real ~/.obey is never touched;
# the captured paths are then normalized to a canonical home so the fixture is
# stable across machines. Nothing else is edited.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

bin="$work/obey-voice"
(cd "$repo" && go build -o "$bin" ./cmd/obey-voice)

# fixture-install/ describes the install these payloads are captured from —
# a config file plus the active persona that puts a real persona block in
# config-get.json. TestCapturedFixturesMatchTheCurrentPayloads builds its
# install from the same directory, so the script and the test cannot end up
# describing two different machines.
export HOME="$work/home"
root="$HOME/.obey/agents/voice/obey-voice"
mkdir -p "$root"
cp -R "$here/fixture-install/." "$root/"

# Two defaults (models_dir, whispercpp_model_path) live under the cache root
# rather than the install root, so both have to be normalized or the fixtures
# carry this run's mktemp path and churn on every re-capture.
canonical='/home/demo/.obey/agents/voice/obey-voice'
canonicalCache='/home/demo/.cache/obey-voice'
normalize() { sed -e "s#$root#$canonical#g" -e "s#$HOME/.cache/obey-voice#$canonicalCache#g"; }

# env -i, because every config key has an environment binding: a capture that
# inherited the operator's shell would freeze one of their exports into the
# contract, and the test compares values now rather than only key names.
run() { env -i HOME="$HOME" PATH="$PATH" "$@"; }

run "$bin" config schema --json | normalize >"$here/config-schema.json"
# The one deliberate export: config-get.json is worth nothing as a decoding
# fixture unless at least one value has source "env".
run env OLLAMA_MODEL=qwen2.5:14b "$bin" config get --json | normalize >"$here/config-get.json"
run "$bin" config get vad_silence_duration --json | normalize >"$here/config-get-key.json"
run "$bin" config set vad_silence_duration 0.9 --json | normalize >"$here/config-set-ok.json"
run "$bin" config set vad_silence_duration 0.9 --json | normalize >"$here/config-set-noop.json"
# An error case exits 1 by design; the payload is what is being captured.
{ run "$bin" config set vad_silence_duration fast --json 2>/dev/null || true; } | normalize >"$here/config-set-error.json"

# The backup path carries a capture timestamp; pin it so re-running the script
# does not churn the fixture. TestCapturedFixturesMatchTheCurrentPayloads applies
# the same substitution to the live payload (capturedBackupSuffix), so the two
# have to stay in step. Written through a temp file rather than sed -i, whose
# in-place flag differs between BSD and GNU.
sed 's#config\.yaml\.bak\.[0-9TZ.]*#config.yaml.bak.20260817T031500.000000000Z#' \
	"$here/config-set-ok.json" >"$work/pinned.json"
mv "$work/pinned.json" "$here/config-set-ok.json"

echo "captured into $here"
