#!/usr/bin/env just --justfile
# Latency benchmarking and the recorded utterance corpus.
#
# Loaded as a module from the root justfile, so justfile_directory() is the repo
# root (the module's own pwd is .justfiles/ — do not build paths from pwd).

repo := justfile_directory()
manifest := repo / "testdata/corpus/manifest.json"

[private]
default:
    @just --list --justfile {{source_file()}}

[doc('Replay labeled addressee cases against baseline or optional decision providers (lab only)')]
decision *ARGS:
    @cd "{{repo}}" && go run ./scripts/decision_bench {{ARGS}}

[doc('Measure and diff against the committed baseline (fails on >10% regression)')]
diff:
    @{{repo}}/scripts/bench.sh diff

[doc('Measure and (re)write docs/benchmarks/baseline.json — defines the reference machine')]
baseline:
    @{{repo}}/scripts/bench.sh baseline

[doc('Measure and print, writing nothing')]
run:
    @{{repo}}/scripts/bench.sh run

[doc('Measure and save a labelled delta artifact, e.g. `just bench save L1`')]
save label:
    @{{repo}}/scripts/bench.sh save {{label}}

[doc('Measure the same fixtures with speech on this computer or another, e.g. `just bench save-place S6 lan`')]
save-place label place:
    @BENCH_TTS_PLACE={{place}} {{repo}}/scripts/bench.sh save {{label}}-{{place}}

[doc('Compare full-turn PlaybackStartElapsed between two artifacts (reported, never gated)')]
place-diff a b:
    @{{repo}}/scripts/bench.sh place-diff {{a}} {{b}}

# Brain-model A/B. Separate from the fixture benches above because it measures
# the model, not the speech stack: it drives brain.ThinkStream directly rather
# than a recorded utterance, so it needs no corpus. Gate and candidate list:
# docs/benchmarks/B1-brain-latency-ab-criteria.md
[doc('Conversational brain-model A/B through the real brain seam (all nine candidates, ~1h)')]
brain *ARGS:
    @cd "{{repo}}" && go run ./scripts/brain_bench {{ARGS}}

# -out goes to a temp file, not /dev/stdout: the harness prints its own summary
# table to stdout, and interleaving the JSON with it makes both unreadable.
[doc('Same A/B, one model and one rep, e.g. `just bench brain-one qwen3:30b`')]
brain-one model:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    out=$(mktemp -t brain_bench).json
    go run ./scripts/brain_bench -models "{{model}}" -reps 1 -rewarm "" -out "$out"
    echo "  artifact: $out"

[doc('List audio input devices for recording')]
devices:
    @{{repo}}/scripts/record-corpus.sh --devices

[doc('Record corpus utterances (interactive). Override input with AUDIO_DEVICE=:N')]
record:
    @{{repo}}/scripts/record-corpus.sh

[doc('Validate the corpus: manifest schema, files present, all categories covered')]
corpus-check:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    manifest="testdata/corpus/manifest.json"
    fail=0

    schema=$(jq -r '.schema // ""' "$manifest")
    [[ "$schema" == "samantha.corpus.v1" ]] || { echo "✗ bad schema: '$schema'"; fail=1; }

    total=$(jq '.samples | length' "$manifest")
    echo "  samples: $total"

    # An unrecorded corpus is a valid state, not a failure: recording is a human
    # step. tests/corpus skips on empty for the same reason - the two validators
    # must agree, or the documented command is red on a fresh clone and people
    # learn to ignore it.
    if [[ "$total" -eq 0 ]]; then
        echo ""
        echo "  corpus not recorded yet — run: just bench record"
        exit 0
    fi

    # Every listed WAV must exist. A manifest referencing a missing file is worse
    # than an empty one: the benchmark would silently score fewer samples.
    while read -r p; do
        [[ -f "testdata/corpus/$p" ]] || { echo "✗ missing file: $p"; fail=1; }
    done < <(jq -r '.samples[].path' "$manifest")

    # Every sample needs an expected transcript or accuracy cannot be scored.
    blank=$(jq '[.samples[] | select((.expect // "") | gsub("^\\s+|\\s+$";"") == "")] | length' "$manifest")
    [[ "$blank" -eq 0 ]] || { echo "✗ $blank sample(s) have an empty expect field"; fail=1; }

    for c in short_command thoughtful_pause noisy_room; do
        n=$(jq --arg c "$c" '[.samples[] | select(.category == $c)] | length' "$manifest")
        echo "  $c: $n"
        if [[ "$n" -eq 0 ]]; then
            echo "✗ category '$c' has no samples"
            fail=1
        fi
    done

    unknown=$(jq -r '[.samples[].category] - ["short_command","thoughtful_pause","noisy_room"] | unique | join(", ")' "$manifest")
    [[ -z "$unknown" ]] || { echo "✗ unknown categories: $unknown"; fail=1; }

    if [[ "$fail" -ne 0 ]]; then
        echo ""
        echo "corpus-check FAILED"
        exit 1
    fi
    echo ""
    echo "✓ corpus valid"

[doc('Print manifest as repeatable --full-turn-fixture/--expect-text benchmark flags')]
corpus-args:
    @cd "{{repo}}" && jq -r '.samples[] | "--full-turn-fixture=testdata/corpus/\(.path)", "--expect-text=\(.expect)"' testdata/corpus/manifest.json

[doc('Measure TTS engines over long-reply fixtures. Usage: just bench tts, or just bench tts "kokoro qwen3-tts:1.7b"')]
tts specs="":
    @{{repo}}/scripts/bench.sh tts {{specs}}

[doc('Aggregate a raw TTS run into a tts-ab decision record. Usage: just bench tts-ab V1 raw.json kokoro qwen3-tts:1.7b')]
tts-ab label raw control candidate:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    # Machine facts are gathered here, the same way scripts/bench.sh does it,
    # so cmd/tts-ab stays a pure JSON-in/JSON-out function that unit tests can
    # drive without a machine.
    #
    # The candidate can carry a tier ("qwen3-tts:1.7b"), which is not a legal
    # filename fragment, so it is flattened for the artifact name only.
    slug="$(printf '%s' "{{candidate}}" | tr ':' '-')"
    out="docs/benchmarks/{{label}}-tts-ab-${slug}-$(date +%Y-%m-%d).json"
    cfg() { ./bin/obey-voice config "$1" 2>/dev/null | sed 's/\x1b\[[0-9;]*m//g' | awk -F' = ' 'NR==1 {gsub(/^[ \t]+/, "", $2); print $2}'; }
    go run ./cmd/tts-ab \
        -in "{{raw}}" \
        -control "{{control}}" \
        -candidate "{{candidate}}" \
        -item "{{label}}" \
        -date "$(date +%Y-%m-%d)" \
        -commit "$(git rev-parse HEAD 2>/dev/null || echo unknown)" \
        -machine "$(sysctl -n hw.model 2>/dev/null || echo unknown)" \
        -cpu "$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)" \
        -os "$(sw_vers -productName 2>/dev/null || uname -s) $(sw_vers -productVersion 2>/dev/null || uname -r)" \
        -backend "${TTS_AB_BACKEND:-}" \
        -brain-provider "${BRAIN_PROVIDER:-$(cfg brain_provider)}" \
        -brain-model "${OLLAMA_MODEL:-$(cfg ollama_model)}" \
        -persona "$(cfg active_persona)" \
        -fixtures "${FIXTURE_KIND:-synthetic long-reply (3 utterances)}" \
        -command "${TTS_AB_COMMAND:-just bench tts}" \
        -out "$out"
    echo "Now write the prose by hand: criteria_fixed_in_advance, reading_the_numbers, evidence_limits, durable_value, and the *_criterion_met verdicts."

[doc('Aggregate a raw benchmark run into an stt-ab decision record. Usage: just bench stt-ab T1 raw.json sherpa-parakeet transcribecpp')]
stt-ab label raw control candidate:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    # Machine facts are gathered here, the same way scripts/bench.sh does it,
    # so cmd/stt-ab stays a pure JSON-in/JSON-out function that unit tests can
    # drive without a machine.
    out="docs/benchmarks/{{label}}-stt-ab-{{candidate}}-$(date +%Y-%m-%d).json"
    go run ./cmd/stt-ab \
        -in "{{raw}}" \
        -control "{{control}}" \
        -candidate "{{candidate}}" \
        -item "{{label}}" \
        -date "$(date +%Y-%m-%d)" \
        -commit "$(git rev-parse HEAD 2>/dev/null || echo unknown)" \
        -machine "$(sysctl -n hw.model 2>/dev/null || echo unknown)" \
        -cpu "$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)" \
        -os "$(sw_vers -productName 2>/dev/null || uname -s) $(sw_vers -productVersion 2>/dev/null || uname -r)" \
        -backend "${STT_AB_BACKEND:-}" \
        -fixtures "${FIXTURE_KIND:-}" \
        -command "${STT_AB_COMMAND:-}" \
        -out "$out"
    echo "Now write the prose by hand: criteria_fixed_in_advance, reading_the_numbers, evidence_limits, durable_value, and the *_criterion_met verdicts."

[doc('Diagnose per-turn brain latency against the raw ollama API (B2). Usage: just bench ttft, or just bench ttft "-cases cache-tail-vary -reps 3"')]
ttft *ARGS:
    @cd "{{repo}}" && go run ./scripts/ttft_probe {{ARGS}}

[doc('Record what a client actually sends ollama. Point the client at it with OLLAMA_HOST=http://127.0.0.1:11500')]
ttft-proxy out=(`printf '%s/ttft-proxy-capture.jsonl' "${TMPDIR:-/tmp}"`):
    @cd "{{repo}}" && go run ./scripts/ttft_probe -proxy 127.0.0.1:11500 -proxy-out "{{out}}"
