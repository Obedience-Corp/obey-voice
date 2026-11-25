#!/usr/bin/env bash
# Launch the real TUI with one disposable persona. No model, audio, or credentials:
# the child gets a fresh HOME and a PATH without provider CLIs.
set -euo pipefail
persona_demo_dir="$(mktemp -d "${TMPDIR:-/tmp}/obey-persona-work.XXXXXX")"
trap 'rm -rf "$persona_demo_dir"' EXIT
persona_demo_work_dir="/tmp/obey-voice-demo-workspace-persona-work"
persona_demo_config="$persona_demo_dir/.obey/agents/voice/obey-voice"
mkdir -p "$persona_demo_work_dir" "$persona_demo_config/personas/lumen"
cat > "$persona_demo_config/config.yaml" <<YAML
brain_provider: ollama
ollama_host: http://127.0.0.1:1
ollama_model: local-model
user_display_name: Alex
work_dir: $persona_demo_work_dir
tts_provider: kokoro
tts_voice: af_heart
active_persona: lumen
agent_name: Lumen
voice_worker_provider: ollama
voice_worker_model: worker-model
voice_tools_enabled: true
YAML
cat > "$persona_demo_config/personas/lumen/persona.yaml" <<'YAML'
schema: obey-voice.persona.v1
id: lumen
display_name: Lumen
brain:
  provider: ollama
  model: local-model
  work: own
tts:
  provider: kokoro
  voice: af_heart
prompts:
  persona: obey
YAML
env -i HOME="$persona_demo_dir" PATH="$PWD/bin:/usr/bin:/bin" TERM=xterm-256color \
    OBEY_VOICE_WORK_DIR="$persona_demo_work_dir" \
    COLORTERM=truecolor CLICOLOR_FORCE=1 FORCE_COLOR=1 OBEY_VOICE_COLOR_PROFILE=truecolor \
    ./bin/obey-voice -t -n || true
echo "lumen/persona.yaml brain:"
grep -E '^ +(work|job_control):' "$persona_demo_config/personas/lumen/persona.yaml" || echo "  (no work or job_control saved)"
