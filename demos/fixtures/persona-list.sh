#!/usr/bin/env bash
# Source this fixture in a demo shell to isolate persona configuration.
persona_demo_home="$(mktemp -d /tmp/obey-persona-list.XXXXXX)"
trap 'rm -rf "$persona_demo_home"' EXIT
persona_demo_config="$persona_demo_home/.obey/agents/voice/obey-voice"
mkdir -p "$persona_demo_config/personas"
cat > "$persona_demo_config/config.yaml" <<'YAML'
active_persona: jarvis
agent_name: Jarvis
brain_provider: ollama
ollama_model: qwen3:8b
tts_provider: kokoro
tts_voice: af_heart
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
  worker:
    provider: "${6:-}"
    model: "${7:-}"
tts:
  provider: kokoro
  voice: $voice
prompts:
  persona: $id
YAML
}
write_persona jarvis Jarvis ollama qwen3:14b bm_george claude claude-sonnet
write_persona obey Obey '' '' af_heart
write_persona samantha Samantha grok grok-4-1-fast-reasoning af_sky
write_persona santa Santa ollama qwen3:4b am_santa
write_persona veronica Veronica ollama qwen3:8b af_bella
export HOME="$persona_demo_home"
