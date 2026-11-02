# Obey Voice docs

The [root README](../README.md) is the first-visit page: what Obey Voice is, how to install it, and how to talk to it.

Everything else lives here.

## Guides

| Page | Topic |
|------|--------|
| [Install](install.md) | Requirements, Homebrew, Linux archive, source builds |
| [Usage](usage.md) | Local voice, remote access, commands, TUI, personas |
| [Chat images](chat-images.md) | Image/GIF paste, previews, model support, and review recording |
| [Narration](narration.md) | `render`, audiobooks, Calibre, narrate pipeline |
| [Scenes](scene.md) | `scene`, scripted two-persona conversation audio |
| [Meetings](meetings.md) | Meeting recorder, speaker labels, campaign routing |
| [Audio8 voices](audio8-voices.md) | Registering reference voices for the audio8 provider, and the consent rules |
| [OpenAI-compatible speech](openai-speech.md) | The `openai-speech` engine: Phonon through Ollama, or any `/v1/audio/speech` server |
| [Configuration](configuration.md) | `config.yaml` keys, Agent Skills |
| [Desktop development](desktop-development.md) | Native app, monorepo builds, CI and isolated UI fixtures |
| [Pipeline debugging](pipeline-debugging.md) | Live conversation inspection and saved captures |
| [Development](development.md) | Build, tests, model assets, benchmarks |

## Reference

| Page | Topic |
|------|--------|
| [Voice stack](voice-stack.md) | Conversation pipeline, speech models, and worker routing |
| [Serve protocol](serve-protocol.md) | Remote serve HTTPS + WebSocket contract |
| [Transcribe protocol](transcribe-protocol.md) | `obey-transcript/v1`: transcribing a file from another program |
| [Speech worker](speech-serve.md) | Offloading speech to another computer (`speech-serve`) |
| [Qwen3-TTS spike](qwen3-tts-spike.md) | Native Qwen3-TTS product path (worker + GGUF) |
| [AEC probe](aec-probe.md) | AEC / voice-frontend probe notes |
| [Optional decision routing](decision-routing.md) | Local setup, independent controls, pipeline diagrams and latency replay |
| [Harness routing design](harness-routing-design.md) | Proposed shared worker routing, provider capability parity, and acceptance gates |
| [ADRs](adr/) | Architecture decision records |
| [Benchmarks](benchmarks/README.md) | Committed latency measurements |
| [Prompts](prompts/P3-README.md) | Prompt fixtures |

## Schemas

- [persona.schema.json](schemas/persona.schema.json)
- [prompt.schema.json](schemas/prompt.schema.json)
