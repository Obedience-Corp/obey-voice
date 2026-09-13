# Testing
BUILDTOOL := "go run ./internal/buildutil"

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Unit + Docker integration only (CI-friendly core).
# Prefer `just test full` for the complete local suite.
[no-cd]
all: unit integration

# Full local suite: unit, Docker integration, voiceflow, speaker diarization
# (fixture + optional VHS), and audio crackle. Requires Docker for integration.
[no-cd]
full: unit integration voiceflow speakerflow audio-crackle
    @echo ""
    @echo "✓ just test full complete (unit + integration + voiceflow + speakerflow + audio-crackle)"

# Run unit tests only with dashboard
[no-cd]
unit:
    @{{BUILDTOOL}} test

# Run tests for a specific package
[no-cd]
pkg PACKAGE:
    go test ./internal/{{PACKAGE}}/... -v

# Run integration tests with dashboard (requires Docker)
[no-cd]
integration:
    @{{BUILDTOOL}} integration

# Run integration tests with verbose output
[no-cd]
integration-verbose:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Building Linux binary for containers..."
    mkdir -p bin/linux
    CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -tags=integration -o bin/linux/obey-voice ./cmd/obey-voice
    echo "Running container-based integration tests..."
    go test -v -tags=integration ./tests/integration/... -timeout 5m 2>&1

# Run fixture-audio voice flow tests (tagged integration, no Docker).
[no-cd]
voiceflow:
    #!/usr/bin/env bash
    set -euo pipefail
    go test -race -tags integration ./tests/voiceflow/... -count=1 -timeout 5m

# Multi-voice meeting diarization (real YouTube fixture + optional VHS GIF).
# Delegates to the root recipe so fixture fetch / demo stay in one place.
[no-cd]
speakerflow:
    just speakerflow

# Software crackle + playback layout regression suite (no hardware).
# Pins Studio Display mono-client crackle class: stereo upmix, expand, and
# underrun/impulse metrics. Also covered by CI: go test -race ./...
[no-cd]
audio-crackle:
    go test -race ./pkg/voiceagent/audio/ -count=1 -run 'Crackle|Expand|Choose|Pick|StudioDisplay|CallbackExpand|MultiChannelExpand|Whole|Chunk|FinalizeSegmentThen|Early|Pump|Analyze|Layout|DebugMetadataRecords'

# Live Kokoro regression: real model, real WAV sample counts for contractions.
# Requires Kokoro assets under models_dir (config.yaml / MODELS_DIR). Skips if missing.
# Pins: U+0329 token alias, no text mangling of wasn't/isn't/don't, full audio vs stock tokens.
[no-cd]
kokoro-contractions:
    go test -tags=integration ./pkg/voiceagent/tts/ -count=1 -run 'LiveKokoro' -timeout 5m -v

# Physical speaker → microphone acceptance. Requires a prebuilt convoflow
# binary and a scenario whose capture.microphone path is enabled. Runs are
# intentionally serial: two acoustic measurements cannot share a room.
[no-cd]
acoustic SCENARIO CONFIG RUNS:
    #!/usr/bin/env bash
    set -euo pipefail
    bin="${CONVOFLOW_BIN:-bin/convoflow}"
    [[ -x "$bin" ]] || { echo "error: $bin is not executable (set CONVOFLOW_BIN to the pinned convoflow binary)" >&2; exit 1; }
    for i in $(seq 1 {{RUNS}}); do
        "$bin" -scenario "{{SCENARIO}}" -fixture-config "{{CONFIG}}"
    done

# Compare two completed acoustic/work latency conditions. This is reported
# evidence only; the harness itself applies the population/provenance checks.
[no-cd]
work-latency-compare BASELINE WORKER:
    go run ./tests/convoflow -compare "{{BASELINE}},{{WORKER}}"

# Opt-in hardware playback on the real default device (Studio Display, etc.).
# Requires speakers; plays a short tone. Use after any pkg/voiceagent/audio change.
[no-cd]
audio-hardware:
    #!/usr/bin/env bash
    set -euo pipefail
    OBEY_VOICE_HARDWARE_AUDIO=1 go test -race ./pkg/voiceagent/audio/ -count=1 -run 'Hardware' -v -timeout 60s

# Build a pinned harness and reproducible local Kokoro fixtures/scenarios.
# Explicit model directory; does not read or change the installed user config.
[no-cd]
acoustic-prepare MODELS_DIR SUITE_DIR:
    #!/usr/bin/env bash
    set -euo pipefail
    suite="{{SUITE_DIR}}"
    mkdir -p "$suite/bin" "$suite/fixtures"
    go build -tags "${CONVOFLOW_GO_TAGS:-}" -o "$suite/bin/convoflow" ./tests/convoflow
    go build -o "$suite/bin/golden-tts" ./cmd/golden-tts
    texts=("What is the capital of France? Answer in one word." "What is two plus two? Answer with just the number." "Say hello in exactly three words." "Count from one to one hundred slowly and say every number.")
    names=(synth-01 synth-02 synth-03 counting)
    for i in 0 1 2 3; do
        "$suite/bin/golden-tts" -models-dir "{{MODELS_DIR}}" -voice af_heart -speed 1 -text "${texts[$i]}" -out "$suite/fixtures/${names[$i]}.wav"
    done
    "$suite/bin/convoflow" -prepare-acoustic-suite "$suite/scenarios" -fixture-dir "$suite/fixtures"
    shasum -a 256 "$suite/bin/convoflow" > "$suite/binary.sha256"

# Collect 3 warmups + 30 physical turns per condition, serially, then compare.
# Use a different explicit CONFIG for each shared/independent endpoint pair.
[no-cd]
acoustic-pair CONFIG SUITE_DIR:
    #!/usr/bin/env bash
    set -euo pipefail
    suite="{{SUITE_DIR}}"
    mkdir -p "$suite/runs"
    pair="$(mktemp -d "$suite/runs/pair.XXXXXX")"
    trap 'echo "Acoustic pair artifacts: $pair"' EXIT
    "$suite/bin/convoflow" -scenario "$suite/scenarios/baseline-33.yaml" -fixture-config "{{CONFIG}}" -runs-dir "$pair/baseline"
    "$suite/bin/convoflow" -scenario "$suite/scenarios/worker-33.yaml" -fixture-config "{{CONFIG}}" -runs-dir "$pair/worker"
    baseline=("$pair"/baseline/*/metrics.json)
    worker=("$pair"/worker/*/metrics.json)
    "$suite/bin/convoflow" -compare "${baseline[0]},${worker[0]}" > "$pair/comparison.json"
    cat "$pair/comparison.json"

# Completion handoff and production barge-in using the prepared fixtures.
[no-cd]
acoustic-controls CONFIG SUITE_DIR:
    #!/usr/bin/env bash
    set -euo pipefail
    suite="{{SUITE_DIR}}"
    for scenario in completion interruption; do
        "$suite/bin/convoflow" -scenario "$suite/scenarios/$scenario.yaml" -fixture-config "{{CONFIG}}" -runs-dir "$suite/runs/controls"
    done

# One physical turn with the explicitly configured TTS provider (e.g. native Qwen).
[no-cd]
acoustic-smoke CONFIG SUITE_DIR:
    "{{SUITE_DIR}}/bin/convoflow" -scenario "{{SUITE_DIR}}/scenarios/smoke.yaml" -fixture-config "{{CONFIG}}" -runs-dir "{{SUITE_DIR}}/runs/smoke"

# Offline detector diagnostics; offsets are seconds from the start of mic-in.wav.
# A successful command means replay completed, not that interruption passed.
[no-cd]
acoustic-vad ARTIFACT CONFIG ARM STIMULUS:
    OBEY_VOICE_VAD_CAPTURE="{{ARTIFACT}}" OBEY_VOICE_VAD_CONFIG="{{CONFIG}}" OBEY_VOICE_VAD_ARM_OFFSET="{{ARM}}" OBEY_VOICE_VAD_STIMULUS_OFFSET="{{STIMULUS}}" go test ./tests/convoflow -run '^TestReplaySavedBargeVAD$' -count=1 -v

# Replay saved ordered capture/reference operations through the current frontend.
# Reports detector behavior; does not replace the physical interruption gate.
[no-cd]
acoustic-reprocess RUN_DIR CONFIG:
    OBEY_VOICE_FRONTEND_REPROCESS_RUN="{{RUN_DIR}}" OBEY_VOICE_VAD_CONFIG="{{CONFIG}}" go test ./tests/convoflow -run '^TestReprocessSavedFrontendTrace$' -count=1 -v

# Real Bubble Tea/pipeline host boundaries with controlled STT, 30 samples each.
# Hardware capture, inference and spoken acknowledgement are separate gates.
[no-cd]
work-host-timing:
    go test -race ./internal/tui -run '^TestWork(AdmissionToNextCaptureHostOverhead|QuietCompletionHostOverhead)$' -count=1 -v -timeout=60s
