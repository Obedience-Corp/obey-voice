#!/usr/bin/env bash
# diar-ab-sortformer.sh — T3 spike: Sortformer vs the current sherpa offline
# meeting-diarization path, on the shared meeting fixture.
#
# Runs both systems on the same wav, records raw outputs + wall-clock timings
# into a raw directory, then aggregates them into the
# `obey-voice.bench.diar-ab.v1` artifact with scripts/diar_ab_aggregate.py.
#
# ---------------------------------------------------------------- prereqs
#
# 1. Fixture (gitignored, shared user cache — NOT committed):
#
#      just fetch-meeting-fixture
#
#    Resolves to ${OBEY_VOICE_FIXTURE_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/festival-voice/fixtures/meetings}/
#    product-marketing-meeting-90s.wav
#      sha256 ed57895c81d4d5164be0ebace0cf4a71098a775caff143527d6d5d12b71816b7
#
# 2. Engine binary with the current diarization path (pyannote-seg-3.0-int8 +
#    NeMo TitaNet, via internal/speaker.SherpaEngine):
#
#      just install     # -> $GOBIN/obey-voice
#
#    Speaker models are fetched on demand into the model cache by
#    config.EnsureRuntimeAssets.
#
# 3. Sortformer GGUF — weights are NOT committed (NVIDIA Open Model License,
#    139 MB). Download once:
#
#      mkdir -p "$SORTFORMER_MODEL_DIR"
#      curl -L -o "$SORTFORMER_MODEL" \
#        https://huggingface.co/handy-computer/diar_streaming_sortformer_4spk-v2.1-gguf/resolve/main/diar_streaming_sortformer_4spk-v2.1-Q8_0.gguf
#
#    Expected sha256:
#      a5dacdc650790266c7a362e54e6bf51952015487edaa606c4e11632bc32442a9
#    (published quants are F32 / F16 / Q8_0 only — no K-quants; see
#     docs/models/diar_streaming_sortformer_4spk-v2.1.md upstream)
#
# 4. transcribe.cpp CLI, pinned v0.2.2 (commit c6a9257), built with Metal:
#
#      cmake -B build && cmake --build build --target transcribe-cli
#
# ------------------------------------------------------------------ usage
#
#   scripts/diar-ab-sortformer.sh [--out-dir DIR] [--raw-dir DIR] [--iters N]
#
# Override any path with the environment variables below.
set -euo pipefail

DATE="${DIAR_AB_DATE:-$(date +%F)}"
ITERS="${DIAR_AB_ITERS:-4}"
RAW_DIR=""
OUT=""

while [[ $# -gt 0 ]]; do
	case "$1" in
	--raw-dir) RAW_DIR="$2"; shift 2 ;;
	--out) OUT="$2"; shift 2 ;;
	--iters) ITERS="$2"; shift 2 ;;
	-h | --help) sed -n '2,50p' "$0"; exit 0 ;;
	*) echo "unknown arg: $1" >&2; exit 2 ;;
	esac
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RAW_DIR="${RAW_DIR:-$repo_root/.diar-ab-raw}"
OUT="${OUT:-$repo_root/docs/benchmarks/T3-diar-ab-sortformer-${DATE}.json}"

# ---- inputs -----------------------------------------------------------------

APP_SLUG="${APP_SLUG:-festival-voice}"
if [[ -n "${OBEY_VOICE_FIXTURE_CACHE:-}" ]]; then
	fixture_cache="$OBEY_VOICE_FIXTURE_CACHE"
else
	fixture_cache="${XDG_CACHE_HOME:-$HOME/.cache}/${APP_SLUG}/fixtures/meetings"
fi
FIXTURE="${OBEY_VOICE_MEETING_FIXTURE:-$fixture_cache/product-marketing-meeting-90s.wav}"

SPIKE_DIR="${TRANSCRIBECPP_SPIKE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/${APP_SLUG}/spikes/transcribecpp}"
SORTFORMER_MODEL_DIR="${SORTFORMER_MODEL_DIR:-$SPIKE_DIR/models}"
SORTFORMER_MODEL="${SORTFORMER_MODEL:-$SORTFORMER_MODEL_DIR/diar_streaming_sortformer_4spk-v2.1-Q8_0.gguf}"
TRANSCRIBE_CLI="${TRANSCRIBE_CLI:-$SPIKE_DIR/src-v0.2.2/build/bin/transcribe-cli}"
OBEY_VOICE_BIN="${OBEY_VOICE_BIN:-${GOBIN:-$HOME/go/bin}/obey-voice}"

EXPECTED_SPEAKERS="${DIAR_AB_EXPECTED_SPEAKERS:-2}"

# Speaker model cache (config.ModelsDir default); the two ONNX files behind the
# current offline path. Hashed into provenance so the reference is pinned too.
SPEAKER_MODELS_DIR="${OBEY_VOICE_MODELS_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/${APP_SLUG}/models}/speaker"
SEG_MODEL="$SPEAKER_MODELS_DIR/pyannote-segmentation-3.0/model.int8.onnx"
EMB_MODEL="$SPEAKER_MODELS_DIR/nemo_en_titanet_small.onnx"

for f in "$FIXTURE" "$SORTFORMER_MODEL" "$TRANSCRIBE_CLI" "$OBEY_VOICE_BIN"; do
	[[ -e "$f" ]] || { echo "missing required input: $f" >&2; echo "see the prereqs block at the top of $0" >&2; exit 1; }
done

mkdir -p "$RAW_DIR"

# `~`-relative rendering so the artifact does not hard-code a home directory.
tilde() { printf '%s' "${1/#$HOME/\~}"; }
sha() { shasum -a 256 "$1" | awk '{print $1}'; }

# Fixture duration in ms (ffprobe), used as the frame-grid extent.
DURATION_MS="$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$FIXTURE" |
	awk '{printf "%d", $1*1000 + 0.5}')"

echo "==> fixture:    $(tilde "$FIXTURE") (${DURATION_MS} ms)"
echo "==> sortformer: $(tilde "$SORTFORMER_MODEL")"
echo "==> raw dir:    $(tilde "$RAW_DIR")"

now_ms() { python3 -c 'import time;print(time.time()*1000)'; }

# ---- run 1: current sherpa offline path -------------------------------------

echo "==> sherpa (pyannote+TitaNet, cpu) x${ITERS}"
sherpa_walls=()
for ((i = 1; i <= ITERS; i++)); do
	s="$(now_ms)"
	"$OBEY_VOICE_BIN" meeting analyze "$FIXTURE" \
		--speakers "$EXPECTED_SPEAKERS" --json \
		--out "$RAW_DIR/sherpa-cpu.analyze.json" >/dev/null 2>"$RAW_DIR/sherpa-cpu.log"
	e="$(now_ms)"
	sherpa_walls+=("$(python3 -c "print(round($e-$s,1))")")
done
echo "    wall_ms: ${sherpa_walls[*]}"

# Same path with expected-speakers Auto, so the artifact's sensitivity claim is
# backed by a recorded run rather than an assertion.
echo "==> sherpa (expected-speakers Auto) x1"
s="$(now_ms)"
"$OBEY_VOICE_BIN" meeting analyze "$FIXTURE" --speakers 0 --json \
	--out "$RAW_DIR/sherpa-cpu-auto.analyze.json" >/dev/null 2>"$RAW_DIR/sherpa-cpu-auto.log"
e="$(now_ms)"
sherpa_auto_wall="$(python3 -c "print(round($e-$s,1))")"
echo "    wall_ms: $sherpa_auto_wall"

# Process-overhead baseline: same binary, 3 s slice. Process start + config +
# model load, so the reader can separate it from diarization compute.
short="$RAW_DIR/short3s.wav"
ffmpeg -y -loglevel error -i "$FIXTURE" -t 3 -ar 16000 -ac 1 "$short"
base_walls=()
for i in 1 2 3; do
	s="$(now_ms)"
	"$OBEY_VOICE_BIN" meeting analyze "$short" --speakers "$EXPECTED_SPEAKERS" \
		--json --out "$RAW_DIR/short3s.analyze.json" >/dev/null 2>&1
	e="$(now_ms)"
	base_walls+=("$(python3 -c "print(round($e-$s,1))")")
done
echo "    overhead baseline (3 s clip) wall_ms: ${base_walls[*]}"

# ---- run 2: Sortformer, every backend x preset -------------------------------
#
# transcribe-cli has NO --preset flag: the streaming operating point is chosen
# by TRANSCRIBE_SORTFORMER_STREAM_PRESET, read by
# transcribe_sortformer_stream_ext_init. --diarize is unrelated (it is the
# moss/granite-plus ASR speaker-attribution flag) and is deliberately not used:
# Sortformer is a diarization-only model driven by a plain run, with segments
# read back from the --batch-jsonl `speakers` array.

echo "$FIXTURE" >"$RAW_DIR/files.txt"
declare -a run_keys=() run_walls=()

for backend in metal cpu; do
	for preset in default very_high_latency high_latency low_latency; do
		key="sortformer-${backend}-${preset}"
		# Only the offline operating point is repeated; the others are
		# indicative single runs (see schema_notes.iteration_counts).
		n=1
		[[ "$preset" == "very_high_latency" ]] && n="$ITERS"
		walls=()
		for ((i = 1; i <= n; i++)); do
			s="$(now_ms)"
			TRANSCRIBE_SORTFORMER_STREAM_PRESET="$preset" \
				"$TRANSCRIBE_CLI" -m "$SORTFORMER_MODEL" \
				--backend "$backend" -q \
				--batch "$RAW_DIR/files.txt" --batch-jsonl \
				>"$RAW_DIR/${key}.jsonl" 2>"$RAW_DIR/${key}.log"
			e="$(now_ms)"
			walls+=("$(python3 -c "print(round($e-$s,1))")")
		done
		echo "    ${key}: wall_ms ${walls[*]}"
		run_keys+=("$key")
		run_walls+=("$(printf '%s\n' "${walls[@]}" | paste -sd, -)")
	done
done

# ---- runs.json --------------------------------------------------------------

{
	printf '{\n'
	printf '  "sherpa-cpu": {"expected_speakers_arg": %s, "wall_ms": [%s]},\n' \
		"$EXPECTED_SPEAKERS" "$(printf '%s\n' "${sherpa_walls[@]}" | paste -sd, -)"
	printf '  "sherpa-cpu-auto": {"expected_speakers_arg": 0, "wall_ms": [%s]},\n' \
		"$sherpa_auto_wall"
	base_csv="$(printf '%s\n' "${base_walls[@]}" | paste -sd, -)"
	printf '  "overhead_baseline": {"measured_on": "3 s slice of the same fixture", "values": [%s], "median": %s, "note": "process start + config + model load; subtract for an approximate compute-only figure"},\n' \
		"$base_csv" \
		"$(python3 -c "import statistics;print(round(statistics.median([$base_csv]),1))")"
	for i in "${!run_keys[@]}"; do
		sep=","
		[[ $i -eq $((${#run_keys[@]} - 1)) ]] && sep=""
		printf '  "%s": {"wall_ms": [%s]}%s\n' "${run_keys[$i]}" "${run_walls[$i]}" "$sep"
	done
	printf '}\n'
} >"$RAW_DIR/runs.json"

# ---- env.json ---------------------------------------------------------------

python3 - "$RAW_DIR/env.json" <<PY
import json, os, sys, subprocess, platform

def sh(*a):
    return subprocess.run(a, capture_output=True, text=True).stdout.strip()

env = {
  "date": "${DATE}",
  "fixture": {"duration_ms": ${DURATION_MS}},
  "provenance": {
    "machine": {
      "model": sh("sysctl", "-n", "machdep.cpu.brand_string") or platform.processor(),
      "arch": platform.machine(),
      "ncpu": int(sh("sysctl", "-n", "hw.ncpu") or 0),
      "memory_gb": int(sh("sysctl", "-n", "hw.memsize") or 0) // (1024**3),
      "os": "macOS " + sh("sw_vers", "-productVersion"),
    },
    "engine_repo": {
      "path": "projects/obey-voice",
      "commit": sh("git", "-C", "${repo_root}", "rev-parse", "HEAD"),
      "branch": sh("git", "-C", "${repo_root}", "rev-parse", "--abbrev-ref", "HEAD"),
      "read_only": True,
      "note": "The spike branch diff vs main touches only cmd/transcribecpp-ab, scripts/stt-ab-*, scripts/diar-ab-* and the benchmark artifacts — internal/speaker and internal/meeting are identical to main, so the installed binary is representative of the current diarization path.",
    },
    "sherpa_runner": {
      "binary": "\$GOBIN/obey-voice",
      "sha256": sh("shasum", "-a", "256", "${OBEY_VOICE_BIN}").split()[0],
      "subcommand": "meeting analyze",
      "code_path": "internal/speaker.SherpaEngine.Diarize -> sherpa OfflineSpeakerDiarization",
      "model_revision": "pyannote-segmentation-3.0-int8+nemo-titanet-small",
      "models": [
        {"file": "\${XDG_CACHE_HOME:-\$HOME/.cache}/festival-voice/models/speaker/pyannote-segmentation-3.0/model.int8.onnx",
         "sha256": sh("shasum", "-a", "256", "${SEG_MODEL}").split()[0] if os.path.exists("${SEG_MODEL}") else None},
        {"file": "\${XDG_CACHE_HOME:-\$HOME/.cache}/festival-voice/models/speaker/nemo_en_titanet_small.onnx",
         "sha256": sh("shasum", "-a", "256", "${EMB_MODEL}").split()[0] if os.path.exists("${EMB_MODEL}") else None},
      ],
      "backend": "cpu (hardcoded provider=cpu, threads=min(NumCPU,4)); clustering threshold hardcoded 0.9",
    },
    "transcribecpp_runner": {
      "pin": "v0.2.2",
      "commit": "c6a9257cdf8e9c6918c0f8f876246db048a22103",
      "cli": "\${XDG_CACHE_HOME:-\$HOME/.cache}/festival-voice/spikes/transcribecpp/src-v0.2.2/build/bin/transcribe-cli",
      "cli_sha256": sh("shasum", "-a", "256", "${TRANSCRIBE_CLI}").split()[0],
      "model": {
        "name": "diar_streaming_sortformer_4spk-v2.1",
        "quant": "Q8_0",
        "file": "\${XDG_CACHE_HOME:-\$HOME/.cache}/festival-voice/spikes/transcribecpp/models/diar_streaming_sortformer_4spk-v2.1-Q8_0.gguf",
        "sha256": sh("shasum", "-a", "256", "${SORTFORMER_MODEL}").split()[0],
        "source": "https://huggingface.co/handy-computer/diar_streaming_sortformer_4spk-v2.1-gguf",
        "license": "NVIDIA Open Model License",
        "published_quants": ["F32", "F16", "Q8_0"],
        "bytes": os.path.getsize("${SORTFORMER_MODEL}"),
        "upstream_der_ami_ihm_q8_0_pct": 14.73,
        "committed": False,
      },
    },
    "fixture": {
      "name": "product-marketing-meeting-90s.wav",
      "resolved_path": "\${XDG_CACHE_HOME:-\$HOME/.cache}/festival-voice/fixtures/meetings/product-marketing-meeting-90s.wav",
      "sha256": sh("shasum", "-a", "256", "${FIXTURE}").split()[0],
      "bytes": os.path.getsize("${FIXTURE}"),
      "format": "pcm_s16le 16000 Hz mono",
      "duration_ms": ${DURATION_MS},
      "fetched_with": "just fetch-meeting-fixture",
      "committed": False,
      "gitignored": True,
      "origin": "first 90 s of https://www.youtube.com/watch?v=lBVtvOpU80Q (Product Marketing Meeting weekly, 2021-06-28)",
      "ground_truth_labels": None,
    },
    "commands": {
      "regenerate": "scripts/diar-ab-sortformer.sh",
      "aggregate": "python3 scripts/diar_ab_aggregate.py --raw <raw-dir> --out <artifact>",
      "verify": "python3 scripts/diar_ab_aggregate.py --raw <raw-dir> --check <artifact>",
      "fetch_fixture": "just fetch-meeting-fixture",
      "sherpa": "obey-voice meeting analyze <fixture> --speakers ${EXPECTED_SPEAKERS} --json --out <path>",
      "sortformer": "echo <fixture> > files.txt && TRANSCRIBE_SORTFORMER_STREAM_PRESET=<preset> transcribe-cli -m <sortformer.gguf> --backend <metal|cpu> -q --batch files.txt --batch-jsonl",
      "preset_selection_note": "transcribe-cli has no --preset flag; the operating point is selected by the TRANSCRIBE_SORTFORMER_STREAM_PRESET env var (values: default | very_high_latency | high_latency | low_latency), read by transcribe_sortformer_stream_ext_init. --diarize is unrelated (it is the moss/granite-plus ASR speaker-attribution flag) and is NOT used here: Sortformer is a diarization-only model driven by a plain run, with segments read back via the batch-jsonl 'speakers' array.",
    },
  },
}
json.dump(env, open(sys.argv[1], "w"), indent=2)
PY

# ---- aggregate --------------------------------------------------------------

python3 "$repo_root/scripts/diar_ab_aggregate.py" --raw "$RAW_DIR" --out "$OUT"
echo "==> wrote $(tilde "$OUT")"
