# Obey Voice

A local voice assistant that starts speaking before the model finishes the thought.

Talk to local models through Ollama, or connect Claude Code, Codex, Cursor, or Grok. Replies are spoken sentence by sentence as they arrive. Interrupt it and it stops.

Use the native macOS app or the terminal. Give each persona its own voice, model, and prompt, then bring several into one conversation and address them by name.

[Get started](#install) · [Choose your brain](#choose-your-brain) · [Group chat](#group-chat) · [Docs](docs/README.md)

![Obey Voice desktop Home with recent conversations and workspace navigation](docs/images/desktop-home.png)

*Native macOS app with sample conversations.*

![Obey Voice TUI in color: animated launcher logo, persona selection, and listening, hearing, and speaking states](docs/images/tui.gif)

*Real terminal interface with scripted voice activity.* [Reproduce the demo](demos/README.md#readme-demo) with `just demo`.

## Choose your brain

The brain is the model that answers you. Ollama is the default provider: run a local model on your own hardware, with local speech recognition and synthesis. Once the models are downloaded, the voice conversation can run locally without a paid AI subscription. Web tools and any remote providers you enable still use the network.

You can also use Claude Code, [Codex](docs/codex.md), Cursor, Grok, or [Muse Spark](docs/muse-spark.md) through their installed CLIs and existing authentication. With a cloud-backed brain, conversation text goes to that provider. Speech recognition and synthesis run locally by default; remote speech is optional.

Choose the provider and model in **Settings → Brain**. For Ollama, start the local server and download a model first, then select it in Obey Voice. Each persona can override the application’s brain and voice settings. See [configuration](docs/configuration.md) and [personas](docs/personas.md).

Use the [Obey daemon provider](docs/obey-daemon.md) to route conversations through
`obey-go-sdk`, with daemon-configured providers, models, and tool permissions.

## Install

The current install path is from source. Prebuilt app downloads are not published yet.

Start with a checkout:

```bash
git clone https://github.com/Obedience-Corp/obey-voice.git
cd obey-voice
```

### Desktop app

On macOS, install Rust 1.95, Go 1.26.1+, Python 3, Xcode Command Line Tools, and [just](https://github.com/casey/just), then run:

```sh
just desktop install
open "$HOME/Applications/Obey Voice Agent.app"
```

The command builds and installs an optimized, locally signed app in `~/Applications`. On first launch, follow guided setup to choose a brain and speech provider and install their models. Allow microphone access when you start a voice conversation. Quit the app before reinstalling.

The app shares the CLI's settings, personas, and session history.

[Desktop screenshots and usage](apps/desktop/README.md) · [Install details](docs/install.md#desktop-app-macos) · [Development](docs/desktop-development.md)

### Terminal (macOS / Linux)

Requires Go 1.26.1+, `just`, and a C compiler. From the repository root:

```bash
just install          # install the CLI and default speech models
obey-voice doctor --voice-devices
obey-voice            # open the launcher
```

Ensure your Go binary directory (`$GOBIN`, or `$(go env GOPATH)/bin` by default) is on `PATH`. On macOS, grant the terminal microphone access under System Settings → Privacy & Security → Microphone. Choose your brain and model in **Settings → Brain** before starting a conversation.

Linux release packaging targets x86_64 glibc with Ubuntu 24.04 as the compatibility floor. [Full install notes](docs/install.md) cover archive layout, Homebrew caveats, musl, and developer builds. Check [releases](https://github.com/Obedience-Corp/obey-voice/releases) for published artifacts.

## Talk

```bash
obey-voice              # launcher → voice TUI
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

A persona is a complete voice agent: its own system prompt, brain model, voice, and optionally its own working folder. Five ship (Obey, Samantha, Jarvis, Veronica, Santa) and `+ Create persona…` in the TUI adds more. With Ollama as the brain, every enabled persona sits in the same conversation.

Say a name anywhere in the sentence and that persona answers: "Jarvis, what do you think?" or "What do you think, Jarvis?" Whoever answered last keeps the floor for follow-ups. Everyone shares one transcript, with each person's and agent's replies labeled.

Agents can pass the floor too. When one asks another a question, that persona answers in its own voice. Each of your turns allows at most three follow-on agent responses with no repeat responder, and your speech always takes priority.

Defaults pair distinct Phonon voices with Obey, Samantha, Jarvis, and Veronica; Santa uses Kokoro `am_santa`. See [shipped personas](docs/personas.md#shipped-personas) for setup and updating existing profiles.

Details: [shared conversations](docs/personas.md#shared-conversations) and [addressing and handing over the floor](docs/context-continuity.md#addressing-and-handing-over-the-floor).

## More ways to use it

| Do this | Start here |
|---------|------------|
| Turn an EPUB, PDF, Markdown, or URL into audio | `obey-voice render book.epub --out-dir out/book` · [narration](docs/narration.md) |
| Record a meeting (STT only, speaker labels) | `obey-voice meeting record` · [meetings](docs/meetings.md) |
| Transcribe a recording from another program | `obey-voice transcribe --request job.json --jsonl` · [protocol](docs/transcribe-protocol.md) |
| Switch prompt + model + voice as one agent | `obey-voice persona use veronica` · [personas](docs/personas.md) |
| Record two personas talking, scripted or improvised from a brief | `obey-voice scene script.yaml --out-dir out/scene` · [scenes](docs/scene.md) |
| Inspect or change a setting | `obey-voice config` · [configuration](docs/configuration.md) |

## Stack

```text
Mic → Voice detection → Speech recognition → Brain → Sentence chunker → Speech synthesis → Speaker
```

Speech starts as sentences arrive. The latency target is under 2 seconds from the end of your utterance to first audio; actual timing depends on your hardware, model, and provider.

| Layer | Default | Options |
|-------|---------|---------|
| Brain | Local models via Ollama | Claude Code, [Codex](docs/codex.md), Cursor, Grok, [Obey daemon](docs/obey-daemon.md) |
| STT | NVIDIA Parakeet TDT (sherpa-onnx) | Whisper (`sherpa`), Zipformer streaming, whisper.cpp |
| TTS | Kokoro (sherpa-onnx) | native Qwen3-TTS, [OpenAI-compatible speech servers](docs/openai-speech.md) |
| VAD | Silero | |
| Audio | miniaudio / malgo | |

Models download on first use into `models_dir`, and `just install` runs `models ensure` for the default Kokoro/STT/VAD stack. Qwen installs from TUI Settings or `obey-voice models ensure --tts` on macOS Apple Silicon (Metal) and linux/amd64 (CPU). The default speech stack and native Qwen3-TTS need no Python runtime.

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

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and PR guidance. [Report a bug or request a feature](https://github.com/Obedience-Corp/obey-voice/issues). For bugs, include your OS, selected brain and speech providers, and relevant error output.

## License

Apache License 2.0. Copyright 2026 Obedience Corp. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

---

Built by [Obedience Corp](https://obediencecorp.com) · [GitHub](https://github.com/Obedience-Corp)

## Inspect a TUI conversation pipeline

In a local terminal conversation, `/record talk` saves a playable
`conversation.wav` containing both microphone input and agent playback, alongside
events, source timing and conversation text. Microphone pause excludes input
audio; interrupted speech includes only the PCM submitted for playback. Pauses
and overlapping speech share one 48 kHz mono timeline, preserving playback bandwidth.
Recording notices include an **Open recording** link to the capture folder. With
mouse reporting enabled, the TUI opens it on click; otherwise your terminal handles
the hyperlink. `/record open` opens the latest capture even without terminal link
support, including after recording stops. `/record` shows the capture ID, link and recording
health. In terminals that support hyperlinks, use the terminal's link-opening
gesture to open the folder. `Ctrl+R` toggles Talk recording outside Vim mode;
`/record lab` includes the same conversation audio and additionally enables
existing audio debug artifacts. The manifest reports unavailable audio sources,
queue loss and recording errors; failed audio leaves the bundle incomplete.
Capture files are local, private to your user, and opt-in. Use `/record off` to seal the bundle.
Clear/new-runtime transitions detach recording; incomplete shutdowns remain
explicitly incomplete.

Open a second terminal with [agent-stream-dbg](https://github.com/Obedience-Corp/agent-stream-dbg):

```sh
agent-stream-dbg observe --dialect obey-voice --follow /absolute/capture/events.jsonl
# Inspect the same saved capture after recording stops:
agent-stream-dbg observe --dialect obey-voice /absolute/capture/events.jsonl
```

The viewer is passive: conversation/persona/work controls remain in Obey Voice.
It shows measured STT, admission, model, synthesis attempts and software playback
intervals, including overlaps, missing boundaries and interrupted stages. Select
an older turn with `[` / `]`, return to latest with `Ctrl+F`, or inspect raw and
normalized evidence with Enter. Background jobs retain their own typed job IDs
and causal parent turn. Viewer exit does not stop the conversation.

The source-owned [trace contract](testdata/pipeline-trace/CONTRACT.md) and
[canonical fixtures](testdata/pipeline-trace/README.md) define nullable clocks,
identity and compatibility. Legacy metrics remain relative; unknown schema
families/versions stay available as raw evidence. Playback timestamps mark
software sink acceptance/completion, not calibrated sound at the listener.
