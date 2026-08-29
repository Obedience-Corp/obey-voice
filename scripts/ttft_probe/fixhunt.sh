#!/usr/bin/env bash
# Fix hunt for B2 (WI-adf58f): does any server-level knob remove qwen3.8's
# per-turn prefill cost?
#
# These are Ollama *server* settings, not request settings, so testing them
# means a server with different environment. Lance's Ollama is not touched: this
# starts a SECOND server on a spare port, which reads the same model blobs
# read-only, measures, unloads, and shuts itself down. Nothing is pulled,
# deleted, created, or written outside the scratch directory.
#
# Usage: fixhunt.sh <probe-binary> <out-dir> [model]
set -euo pipefail

PROBE="${1:?usage: fixhunt.sh <probe-binary> <out-dir> [model]}"
OUTDIR="${2:?usage: fixhunt.sh <probe-binary> <out-dir> [model]}"
MODEL="${3:-qwen3.8-uncensored:27b}"
PORT="${FIXHUNT_PORT:-11499}"
HOST="127.0.0.1:${PORT}"
CASES="${FIXHUNT_CASES:-cache-tail-vary,cache-exact-repeat}"
REPS="${FIXHUNT_REPS:-3}"

mkdir -p "$OUTDIR"

# Every variant is one server lifetime: start, measure, unload, stop. Sharing a
# server between variants would mean measuring a runner built under the previous
# variant's settings.
run_variant() {
    local name="$1"; shift
    echo ""
    echo "=== variant: $name ==="
    echo "    env: $*"

    local log="$OUTDIR/serve-$name.log"
    ( env "$@" OLLAMA_HOST="$HOST" ollama serve >"$log" 2>&1 ) &
    local pid=$!

    local ready=0
    for _ in $(seq 1 60); do
        if curl -sf "http://$HOST/api/version" >/dev/null 2>&1; then ready=1; break; fi
        sleep 1
    done
    if [[ "$ready" != 1 ]]; then
        echo "    server did not come up; see $log"
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
        return 0
    fi

    OLLAMA_HOST="$HOST" "$PROBE" \
        -models "$MODEL" -controls "" -reps "$REPS" \
        -cases "$CASES" -rewarm "" \
        -out "$OUTDIR/fix-$name.json" || true

    # Give the 17 GB back before the next variant loads its own copy.
    curl -s "http://$HOST/api/generate" \
        -d "{\"model\":\"$MODEL\",\"keep_alive\":0}" >/dev/null 2>&1 || true
    sleep 2
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    sleep 3
}

# Control first: a second server with default settings must reproduce the
# number measured on Lance's server, or nothing else in this file means
# anything.
run_variant baseline           OLLAMA_KEEP_ALIVE=5m
run_variant flashattn          OLLAMA_KEEP_ALIVE=5m OLLAMA_FLASH_ATTENTION=1
run_variant flashattn-kv-q8    OLLAMA_KEEP_ALIVE=5m OLLAMA_FLASH_ATTENTION=1 OLLAMA_KV_CACHE_TYPE=q8_0
run_variant parallel1          OLLAMA_KEEP_ALIVE=5m OLLAMA_NUM_PARALLEL=1

echo ""
echo "fix hunt complete; artifacts in $OUTDIR"
