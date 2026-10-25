#!/usr/bin/env bash
# Launch the real TUI with a disposable catalog. No model, audio, or credentials.
set -euo pipefail
persona_demo_dir="$(mktemp -d "${TMPDIR:-/tmp}/obey-persona-models.XXXXXX")"
trap 'rm -rf "$persona_demo_dir"' EXIT
persona_demo_config="$persona_demo_dir/.obey/agents/voice/obey-voice"
mkdir -p "$persona_demo_config/personas"
cat > "$persona_demo_config/config.yaml" <<'YAML'
brain_provider: ollama
ollama_host: http://127.0.0.1:1
ollama_model: qwen3:8b
grok_model: grok-4-1-fast-reasoning
cursor_model: auto
tts_provider: kokoro
tts_voice: af_heart
active_persona: jarvis
agent_name: Jarvis
voice_worker_provider: ollama
YAML
write_persona() {
    local id="$1" name="$2" provider="$3" model="$4" voice="$5"
    mkdir -p "$persona_demo_config/personas/$id"
    cat > "$persona_demo_config/personas/$id/persona.yaml" <<YAML
schema: obey-voice.persona.v1
id: $id
display_name: $name
brain:
  provider: "$provider"
  model: "$model"
tts:
  provider: kokoro
  voice: $voice
prompts:
  persona: obey
YAML
}
write_persona jarvis Jarvis ollama qwen3:14b bm_george
write_persona researcher Researcher grok grok-4-1-fast-reasoning af_bella
write_persona writer Writer claude saved-but-not-routed af_sky
write_persona obey Obey '' '' af_heart
# Explicitly seed the other shipped slots so the recording's catalog is stable.
write_persona samantha Samantha '' '' af_sky
write_persona veronica Veronica ollama qwen3:8b af_bella
write_persona santa Santa ollama qwen3:4b am_santa
# Isolate only the child application's home; retain the calling shell environment.
env -i HOME="$persona_demo_dir" PATH="$PATH" TERM=xterm-256color \
    COLORTERM=truecolor CLICOLOR_FORCE=1 FORCE_COLOR=1 OBEY_VOICE_COLOR_PROFILE=truecolor \
    ./bin/obey-voice -t -n
