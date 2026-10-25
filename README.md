# Obey Voice

A local voice assistant that starts speaking before the model finishes the thought.

Speech stays on your machine. The brain is Claude, Codex, Cursor, Ollama, or Grok. Replies are split into sentences and spoken as soon as each one is ready. Interrupt it and it stops.

Use the [desktop app](#desktop-app) or the terminal interface below.

It is also a group chat for agents. Put several personas in one conversation, each with its own voice, model and prompt, address whichever one you want by name, and let them hand the floor to each other.

![Obey Voice TUI in color: animated launcher logo, persona selection, and listening, hearing, and speaking states](docs/images/tui.gif)

*Recorded from the real TUI with scripted voice activity; no live microphone or model required.*
[Reproduce the demo](demos/README.md#readme-demo) with `just demo`.

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

[Full install notes](docs/install.md) cover the desktop app, Homebrew caveats, the Linux archive, musl, and developer builds.

## Desktop app

Talk to your agents in a native window, resume conversations, manage personas,
and configure models and voices. It shares the CLI's settings and session history.

![Obey Voice desktop Home with recent conversations and workspace navigation](docs/images/desktop-home.png)

*Real macOS app in sample-data preview mode.*

From a full checkout on macOS (Rust 1.95, Go 1.26.1+, Python 3, Xcode Command Line
Tools, and `just`, with access to the private Go dependencies):

```sh
just desktop install
open "$HOME/Applications/Obey Voice Agent.app"
```

The command builds and installs an optimized, locally signed app in
`~/Applications`. On first launch, follow guided setup to choose a brain and
speech provider and install their models. Allow microphone access when you
start a voice conversation. Quit the app before reinstalling.

[Desktop screenshots and usage](apps/desktop/README.md) ·
[Install details](docs/install.md#desktop-app-macos) ·
[Development](docs/desktop-development.md)

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

## Group chat

A persona is a complete voice agent: its own system prompt, brain model, voice, and optionally its own working folder. Four ship (Samantha, Jarvis, Veronica, Obey) and `+ Create persona…` in the TUI adds more. With Ollama as the brain, every enabled persona sits in the same conversation.

Say a name anywhere in the sentence and that persona answers: "Jarvis, what do you think?" or "What do you think, Jarvis?" The local model classifies who you were talking to, so a mention, a quote, or a question *about* a persona does not change who responds. Whoever answered last keeps the floor for follow-ups.

Agents pass the floor too. When one persona's reply asks another a question, that persona answers next in the same thread, in its own voice. Each of your turns allows at most three follow-on agent responses with no repeat responder, and your speech always takes priority, so a thread cannot run away.

One transcript is shared. Each persona sees the others' replies as labeled context (`Jarvis agent:`, `Lance user:`). Switching never relabels earlier replies, and the same routing applies with `--text`. Addressing costs one extra, tools-free model call before each reply.

Details: [shared conversations](docs/personas.md#shared-conversations) and [addressing and handing over the floor](docs/context-continuity.md#addressing-and-handing-over-the-floor).

## Also

| Do this | Start here |
|---------|------------|
| Turn an EPUB, PDF, Markdown, or URL into audio | `obey-voice render book.epub --out-dir out/book` · [narration](docs/narration.md) |
| Record a meeting (STT only, speaker labels) | `obey-voice meeting record` · [meetings](docs/meetings.md) |
| Transcribe a recording from another program | `obey-voice transcribe --request job.json --jsonl` · [protocol](docs/transcribe-protocol.md) |
| Switch prompt + model + voice as one agent | `obey-voice persona use veronica` · [personas](docs/personas.md) |
| Record two personas talking, scripted or improvised from a brief | `obey-voice scene script.yaml --out-dir out/scene` · [scenes](docs/scene.md) |
| Inspect or change a setting | `obey-voice config` · [configuration](docs/configuration.md) |

## Stack

| Layer | Default | Options |
|-------|---------|---------|
| Brain | Ollama | Claude CLI, [Codex](docs/codex.md), Cursor, Grok |
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
| [Personas](docs/personas.md) | Voice agents, shared conversations, starting directories |
| [Scenes](docs/scene.md) | Two-persona conversation recordings |
| [Narration](docs/narration.md) | Render, audiobooks, Calibre, narrate pipeline |
| [Meetings](docs/meetings.md) | Recorder, speaker labels, campaign routing |
| [Configuration](docs/configuration.md) | `config.yaml`, Agent Skills |
| [Development](docs/development.md) | Build, tests, models, benchmarks |
| [Serve protocol](docs/serve-protocol.md) | HTTPS + WebSocket contract |
| [Transcribe protocol](docs/transcribe-protocol.md) | `obey-transcript/v1` file transcription contract |
| [Speech worker](docs/speech-serve.md) | Speech on another computer (`speech-serve`) |
| [ADRs](docs/adr/) | Architecture decisions |

## License

Apache License 2.0. Copyright 2026 Obedience Corp. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

---

Built by [Obedience Corp](https://obediencecorp.com) · [GitHub](https://github.com/Obedience-Corp)
