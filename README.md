# Obey Voice

The voice engine behind the Obey Voice apps (formerly Samantha — she still ships as a persona; the default is now **obey**). A local voice assistant that starts speaking before the model finishes the thought.

Speech stays on your machine. The brain is Claude, Ollama, or Grok. Replies are split into sentences and spoken as soon as each one is ready. Interrupt it and it stops.

![Obey Voice conversation TUI](docs/images/tui-samantha.gif)

```text
Mic → VAD → STT → Brain → Sentence chunker → TTS → Speaker
```

Target: under 2 seconds from the end of your utterance to first audio.

## Install

**macOS / Linux from source** (Go 1.26+, [just](https://github.com/casey/just), a C compiler):

```bash
just install          # binary into $GOBIN, then default STT/TTS/VAD assets (`models ensure`)
```

Grant the terminal microphone access under System Settings → Privacy & Security → Microphone.

**Linux (x86_64 glibc)**

Download `obey-voice-linux-amd64.tar.gz` from the [latest release](https://github.com/Obedience-Corp/obey-voice/releases/latest), then:

```bash
tar -xzf obey-voice-linux-amd64.tar.gz
mkdir -p ~/.local/opt ~/.local/bin
mv obey-voice-linux-amd64 ~/.local/opt/obey-voice
ln -s ~/.local/opt/obey-voice/obey-voice ~/.local/bin/obey-voice
```

Keep the binary next to `lib/`. Ubuntu 24.04 is the compatibility floor. Then:

```bash
obey-voice models ensure
obey-voice doctor --voice-devices
```

[Full install notes](docs/install.md) cover Homebrew caveats, the Linux archive, musl, and developer builds.

## Talk

```bash
obey-voice            # launcher → voice TUI
obey-voice --text       # type, it speaks
obey-voice --no-voice   # speak, it prints
```

Need it on a phone? Open the TUI, choose **Use on another device**, pick Tailscale or same Wi-Fi, open the link.

Headless:

```bash
obey-voice serve --tailscale
```

[Usage](docs/usage.md) has TUI keys, personas, remote HTTPS, and the full command list.

## Also

| Do this | Start here |
|---------|------------|
| Turn an EPUB, PDF, Markdown, or URL into audio | `obey-voice render book.epub --out-dir out/book` · [narration](docs/narration.md) |
| Record a meeting (STT only, speaker labels) | `obey-voice meeting record` · [meetings](docs/meetings.md) |
| Switch prompt + model + voice as one agent | `obey-voice persona use samantha` · [personas](docs/usage.md#personas-voice-agents) |
| Inspect or change a setting | `obey-voice config` · [configuration](docs/configuration.md) |

## Stack

| Layer | Default | Options |
|-------|---------|---------|
| Brain | Ollama | Claude CLI, Grok |
| STT | NVIDIA Parakeet TDT (sherpa-onnx) | Whisper (`sherpa`), Zipformer streaming, whisper.cpp |
| TTS | Kokoro (sherpa-onnx) | native Qwen3-TTS |
| VAD | Silero | |
| Audio | miniaudio / malgo | |

Models download on first use into `models_dir`, and `just install` runs `models ensure` for the default Kokoro/STT/VAD stack. Qwen installs from TUI Settings or `obey-voice models ensure --tts` on macOS Apple Silicon (Metal) and linux/amd64 (CPU). No product Python runtime.

## Docs

| Page | What's there |
|------|----------------|
| [docs/](docs/README.md) | Index |
| [Install](docs/install.md) | Homebrew, Linux archive, source, requirements |
| [Usage](docs/usage.md) | TUI, remote serve, commands, personas |
| [Narration](docs/narration.md) | Render, audiobooks, Calibre, narrate pipeline |
| [Meetings](docs/meetings.md) | Recorder, speaker labels, campaign routing |
| [Configuration](docs/configuration.md) | `config.yaml`, Agent Skills |
| [Development](docs/development.md) | Build, tests, models, benchmarks |
| [Serve protocol](docs/serve-protocol.md) | HTTPS + WebSocket contract |
| [Speech worker](docs/speech-serve.md) | Speech on another computer (`speech-serve`) |
| [ADRs](docs/adr/) | Architecture decisions |

## License

Apache License 2.0. Copyright 2026 Obedience Corp. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

---

Built by [Obedience Corp](https://obediencecorp.com) · [GitHub](https://github.com/Obedience-Corp)
