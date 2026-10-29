#!/usr/bin/env bash
# WI-26ae84 spike 1 driver: Parakeet TDT 0.6B v2 on transcribe.cpp (ggml)
# vs sherpa-parakeet CPU, same fixtures, same scorer.
#
# Control runs twice on purpose:
#   raw-control-realtime.json  --fixture-realtime=true, the L4 basis — audio
#                              replays in real time, so STTFinalElapsed is
#                              dominated by utterance length + the VAD floor.
#                              Anchors this machine/config against L4.
#   raw-control-instant.json   --fixture-realtime=false — compute-only basis,
#                              the only control comparable to the CLI's
#                              per-file mel+encode+decode numbers.
# The aggregator is fed instant control + one candidate at a time; realtime
# numbers and the non-speech hallucination records go into the final T1 prose.
#
# Env:
#   TRANSCRIBECPP_CLI    path to transcribe-cli (default: spike cache build)
#   TRANSCRIBECPP_MODEL  path to the Q8_0 GGUF   (default: spike cache)
#   STT_AB_OUT           work dir for raw files  (default: scratch under /tmp)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SPIKE="${XDG_CACHE_HOME:-$HOME/.cache}/obey-voice/spikes/transcribecpp"
CLI="${TRANSCRIBECPP_CLI:-$SPIKE/src-v0.2.2/build/bin/transcribe-cli}"
MODEL="${TRANSCRIBECPP_MODEL:-$SPIKE/models/parakeet-tdt-0.6b-v2-Q8_0.gguf}"
FIXTURE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/obey-voice/fixtures/stt-ab"
OUT="${STT_AB_OUT:-$SPIKE/run-$(date +%Y-%m-%d)}"
ITERATIONS=3

[[ -x "$CLI" ]] || { echo "transcribe-cli not found at $CLI" >&2; exit 1; }
[[ -f "$MODEL" ]] || { echo "GGUF not found at $MODEL" >&2; exit 1; }
mkdir -p "$OUT"

echo "== fixtures"
go run ./cmd/transcribecpp-ab -synth-only -fixture-dir "$FIXTURE_DIR"

FIXTURE_ARGS=()
i=1
while IFS= read -r text; do
  [[ -z "$text" || "$text" == \#* ]] && continue
  FIXTURE_ARGS+=(--audio-fixture "$FIXTURE_DIR/$(printf 'fix%02d.wav' "$i")" --expect-text "$text")
  i=$((i + 1))
done < scripts/stt-ab-fixtures.txt

echo "== control: sherpa-parakeet, realtime (L4 basis)"
go run ./cmd/obey-voice benchmark --stt-provider sherpa-parakeet \
  --iterations "$ITERATIONS" "${FIXTURE_ARGS[@]}" \
  --json-file "$OUT/raw-control-realtime.json"

echo "== control: sherpa-parakeet, instant feed (compute basis)"
go run ./cmd/obey-voice benchmark --stt-provider sherpa-parakeet \
  --fixture-realtime=false \
  --iterations "$ITERATIONS" "${FIXTURE_ARGS[@]}" \
  --json-file "$OUT/raw-control-instant.json"

echo "== control: sherpa-parakeet on non-speech (hallucination check)"
# Expected to fail: no final ever arrives on non-speech, so the harness exits
# non-zero with 'stt timed out'. That IS the pass condition here; any other
# failure is real and must stop the run.
set +e
NONSPEECH_LOG="$OUT/control-nonspeech.log"
go run ./cmd/obey-voice benchmark --stt-provider sherpa-parakeet \
  --fixture-realtime=false --iterations 1 \
  --audio-fixture "$FIXTURE_DIR/nonspeech-silence.wav" --expect-text "" \
  --audio-fixture "$FIXTURE_DIR/nonspeech-noise.wav" --expect-text "" \
  --json-file "$OUT/raw-control-nonspeech.json" 2>&1 | tee "$NONSPEECH_LOG"
NONSPEECH_RC=${PIPESTATUS[0]}
set -e
if [[ "$NONSPEECH_RC" -ne 0 ]] && ! grep -qi 'stt timed out' "$NONSPEECH_LOG"; then
  echo "non-speech control failed for an unexpected reason (rc=$NONSPEECH_RC); see $NONSPEECH_LOG" >&2
  exit 1
fi

for backend in cpu metal; do
  echo "== candidate: transcribecpp $backend"
  go run ./cmd/transcribecpp-ab -fixture-dir "$FIXTURE_DIR" \
    -cli "$CLI" -model "$MODEL" -backend "$backend" \
    -label "transcribecpp-$backend" -iterations "$ITERATIONS" \
    -out "$OUT/raw-transcribecpp-$backend.json"
done

echo "== merge (instant control + candidates)"
python3 - "$OUT" <<'EOF'
import json, sys, pathlib
out = pathlib.Path(sys.argv[1])
merged = []
for name in ("raw-control-instant.json", "raw-transcribecpp-cpu.json", "raw-transcribecpp-metal.json"):
    data = json.loads((out / name).read_text())
    merged.extend(data["results"] if isinstance(data, dict) else data)
(out / "raw-combined.json").write_text(json.dumps(merged, indent=1) + "\n")
print(f"raw-combined.json: {len(merged)} records")
EOF

echo
echo "Aggregate with:"
echo "  STT_AB_BACKEND=metal FIXTURE_KIND='synthetic (14 utterances, re-synthesized from scripts/stt-ab-fixtures.txt)' \\"
echo "    just bench stt-ab T1 $OUT/raw-combined.json sherpa-parakeet transcribecpp-metal"
echo "then merge the cpu run and write the prose by hand."
