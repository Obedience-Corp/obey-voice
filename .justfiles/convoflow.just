#!/usr/bin/env just --justfile
# convoflow — opt-in live conversation flow.
# Not part of `just test` / `just test full` / CI.
#
# Default is the local product: one Ollama daemon, live + worker as two
# clients of the same model. Swap models here or on the CLI:
#   just convoflow run live_model=qwen3.8:27b
#   just convoflow run live_model=qwen3.8:27b worker_model=qwen3:30b
#   just convoflow run worker_provider=grok          # comparison, not the gate

live_provider := "ollama"
live_model := "qwen3:8b"
worker_provider := "ollama"
worker_model := ""
worker_ollama_host := ""
ollama_host := "http://localhost:11434"
timeout_sec := "1200"
prune_days := "14"

repo := justfile_directory()

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

[no-cd]
run scenario="search-web-intent" live_provider=live_provider live_model=live_model worker_provider=worker_provider worker_model=worker_model worker_ollama_host=worker_ollama_host ollama_host=ollama_host timeout_sec=timeout_sec *flags="":
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    python3 tests/convoflow/watchdog.py "{{timeout_sec}}" go run ./tests/convoflow \
      -scenario "tests/convoflow/scenarios/{{scenario}}.yaml" \
      -live-provider "{{live_provider}}" \
      -live-model "{{live_model}}" \
      -worker-provider "{{worker_provider}}" \
      -worker-model "{{worker_model}}" \
      -worker-ollama-host "{{worker_ollama_host}}" \
      -ollama-host "{{ollama_host}}" \
      {{flags}}

[no-cd]
gate live_provider=live_provider live_model=live_model worker_provider=worker_provider worker_model=worker_model worker_ollama_host=worker_ollama_host ollama_host=ollama_host timeout_sec=timeout_sec:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ "{{worker_provider}}" == "stub" ]]; then
      echo "gate refuses stub worker; pass worker_provider=ollama" >&2
      exit 2
    fi
    ran=0
    skipped=0
    for scn in search-web-intent chat-then-work job-status; do
      set +e
      just --justfile "{{justfile()}}" --working-directory "{{justfile_directory()}}" \
        convoflow run scenario="$scn" \
        live_provider="{{live_provider}}" live_model="{{live_model}}" \
        worker_provider="{{worker_provider}}" worker_model="{{worker_model}}" \
        worker_ollama_host="{{worker_ollama_host}}" ollama_host="{{ollama_host}}" \
        timeout_sec="{{timeout_sec}}"
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

[no-cd]
live-router live_provider=live_provider live_model=live_model worker_provider=worker_provider worker_model=worker_model worker_ollama_host=worker_ollama_host ollama_host=ollama_host timeout_sec=timeout_sec *flags="":
    just --justfile "{{justfile()}}" --working-directory "{{justfile_directory()}}" \
      convoflow run scenario=live-router-spawn \
      live_provider="{{live_provider}}" live_model="{{live_model}}" \
      worker_provider="{{worker_provider}}" worker_model="{{worker_model}}" \
      worker_ollama_host="{{worker_ollama_host}}" ollama_host="{{ollama_host}}" \
      timeout_sec="{{timeout_sec}}" {{flags}}

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
