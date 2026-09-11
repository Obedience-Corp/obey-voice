#!/usr/bin/env just --justfile
# convoflow — opt-in live conversation flow.
# Not part of `just test` / `just test full` / CI.
#
# Default is the local product: one Ollama daemon, live + worker as two
# clients of the same model. The knobs below are the defaults; override one
# with its CONVOFLOW_* environment variable, or pass harness flags after the
# scenario name (the last flag wins). `just` has no named recipe arguments,
# so `run live_model=x` is not a thing — the flag form is:
#   just convoflow run search-web-intent -live-model=qwen3.8:27b
#   just convoflow run search-web-intent -live-model=qwen3.8:27b -worker-model=qwen3:30b
#   CONVOFLOW_LIVE_MODEL=qwen3.8:27b just convoflow gate
#   just convoflow run search-web-intent -worker-provider=grok     # comparison, not the gate

live_provider := env("CONVOFLOW_LIVE_PROVIDER", "ollama")
live_model := env("CONVOFLOW_LIVE_MODEL", "qwen3:8b")
worker_provider := env("CONVOFLOW_WORKER_PROVIDER", "ollama")
worker_model := env("CONVOFLOW_WORKER_MODEL", "")
worker_ollama_host := env("CONVOFLOW_WORKER_OLLAMA_HOST", "")
ollama_host := env("CONVOFLOW_OLLAMA_HOST", "http://localhost:11434")
timeout_sec := env("CONVOFLOW_TIMEOUT_SEC", "1200")
prune_days := "14"

repo := justfile_directory()

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# One scenario by name. Anything after it is a harness flag and overrides the knobs.
[no-cd]
run scenario="search-web-intent" *flags="":
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    # Build, then exec: `go run` exits 1 for any non-zero child, which would
    # turn every skip (exit 2) into a failure before gate could see it.
    bin="$(mktemp -d)/convoflow"
    trap 'rm -rf "$(dirname "$bin")"' EXIT
    go build -o "$bin" ./tests/convoflow
    python3 tests/convoflow/watchdog.py "{{timeout_sec}}" "$bin" \
      -scenario "tests/convoflow/scenarios/{{scenario}}.yaml" \
      -live-provider "{{live_provider}}" \
      -live-model "{{live_model}}" \
      -worker-provider "{{worker_provider}}" \
      -worker-model "{{worker_model}}" \
      -worker-ollama-host "{{worker_ollama_host}}" \
      -ollama-host "{{ollama_host}}" \
      {{flags}}

# v1 success: three Layer-1 scenarios. Exit 1 fails fast; skips (exit 2) continue; all skipped exits 2; stub worker refused.
[no-cd]
gate *flags="":
    #!/usr/bin/env bash
    set -euo pipefail
    ran=0
    skipped=0
    for scn in search-web-intent chat-then-work job-status; do
      set +e
      just --justfile "{{justfile()}}" --working-directory "{{justfile_directory()}}" \
        convoflow run "$scn" -gate {{flags}}
      st=$?
      set -e
      case "$st" in
        0) ran=$((ran+1)) ;;
        2) skipped=$((skipped+1)); echo "skipped $scn" ;;
        *) exit "$st" ;;
      esac
    done
    if [[ "$ran" -eq 0 ]]; then
      echo "just convoflow gate: all $skipped scenario(s) skipped" >&2
      exit 2
    fi

# Soft: the live brain may route(spawn) or chat; only the unavailability phrase fails.
[no-cd]
live-router *flags="":
    just --justfile "{{justfile()}}" --working-directory "{{justfile_directory()}}" \
      convoflow run live-router-spawn {{flags}}

# Shared-persona proof. The fixture config and catalog are explicit inputs so
# the proof never consults process-global config state or the operator's home.
[no-cd]
persona-proof *flags="":
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    : "${CONVOFLOW_FIXTURE_CONFIG:?set CONVOFLOW_FIXTURE_CONFIG to an isolated config file}"
    : "${CONVOFLOW_PERSONA_DIR:?set CONVOFLOW_PERSONA_DIR to the fixture persona catalog}"
    runs="${CONVOFLOW_PERSONA_RUNS_DIR:-${TMPDIR:-/tmp}/convoflow-persona-$(date +%Y%m%dT%H%M%S)}"
    mkdir -p "$runs"
    bin_dir="$(mktemp -d)"
    trap 'rm -rf "$bin_dir"; echo "persona-proof artifacts: $runs"' EXIT
    bin="$bin_dir/convoflow"
    go build -o "$bin" ./tests/convoflow
    shasum -a 256 "$bin" > "$runs/binary.sha256"
    python3 tests/convoflow/watchdog.py "{{timeout_sec}}" "$bin" \
      -scenario tests/convoflow/scenarios/persona-proof.yaml \
      -fixture-config "$CONVOFLOW_FIXTURE_CONFIG" \
      -persona-dir "$CONVOFLOW_PERSONA_DIR" \
      -runs-dir "$runs" \
      -live-provider "{{live_provider}}" \
      -live-model "{{live_model}}" \
      -worker-provider "{{worker_provider}}" \
      -worker-model "{{worker_model}}" \
      -worker-ollama-host "{{worker_ollama_host}}" \
      -ollama-host "{{ollama_host}}" \
      {{flags}}

[no-cd]
prune days=prune_days:
    #!/usr/bin/env bash
    set -euo pipefail
    root="${OBEY_VOICE_CONVOFLOW_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/obey-voice/convoflow}"
    [[ -d "$root" ]] || exit 0
    find "$root" -mindepth 1 -maxdepth 1 -type d -mtime +{{days}} -print -exec rm -rf {} +

[no-cd]
analyze RUN_DIR:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    go run ./tests/convoflow -analyze "{{RUN_DIR}}"
