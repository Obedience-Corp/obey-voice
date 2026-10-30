<h1 align="center">Obey Voice</h1>

<p align="center">
  <strong>Fast, private, actually useful.</strong><br>
  Voice chat with your AI agents, built around local models.
</p>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26.1%2B-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.26.1+"></a>
  <a href="#install"><img src="https://img.shields.io/badge/platforms-macOS%20%7C%20Linux-555" alt="Platforms: macOS and Linux"></a>
  <a href="https://github.com/Obedience-Corp/festival"><img src="https://img.shields.io/badge/built%20with-Festival-7c3aed.svg" alt="Built with Festival"></a>
</p>

<p align="center">
  A voice agent experimentation lab with a native macOS app and a terminal interface.
</p>

<p align="center">
  <a href="#install">Get started</a> ·
  <a href="#the-lab">Explore the lab</a> ·
  <a href="#choose-your-brain">Choose your brain</a> ·
  <a href="#group-chat">Group chat</a> ·
  <a href="docs/README.md">Docs</a>
</p>

<p align="center">
  <img src="docs/images/desktop-home.png" alt="Obey Voice desktop Home with recent conversations and workspace navigation" width="900"><br>
  <em>Native macOS app with sample conversations.</em>
</p>

<p align="center">
  <img src="docs/images/tui.gif" alt="Obey, Jarvis, and Veronica sharing one conversation in the Obey Voice terminal interface" width="900"><br>
  <em>Three personas, one conversation. Real terminal UI with fixture replies.</em><br>
  <a href="demos/README.md#readme-demo">Reproduce the demo</a> with <code>just demo</code>.
</p>

---

## The lab

I'm building Obey Voice to find out how useful voice agents can be when the conversation runs locally. The goal is fast, private voice chat that can get work done, with optional third-party agents for tasks that need them.

It speaks as sentences arrive, stops when you interrupt, and lets you give each agent its own voice and personality. You can swap models, try different routing decisions, and measure what changes.

## Choose your brain

Ollama is the default. With local speech models, conversation and routing can run on your hardware after setup. You can also connect Claude Code, [Codex](docs/codex.md), Cursor, Grok, [Muse Spark](docs/muse-spark.md), or the [Obey daemon](docs/obey-daemon.md).

Keep conversation local and use another provider for background work, or choose a remote brain for the whole conversation. Remote providers receive the text you send them; web tools also use the network. Choose your model in **Settings → Brain**. [Provider and worker settings](docs/configuration.md).

## Install

See the [installation guide](docs/install.md) for app and terminal setup. To build from source, start with a checkout:

```bash
git clone https://github.com/Obedience-Corp/obey-voice.git
cd obey-voice
```

### macOS app

Install Rust 1.95, Go 1.26.1+, Python 3, Xcode Command Line Tools, and [just](https://github.com/casey/just).

```sh
just desktop install
open "$HOME/Applications/Obey Voice Agent.app"
```

Follow first-launch setup to choose providers and download models. Allow microphone access when prompted. Quit the app before reinstalling. [Desktop install guide](docs/install.md#desktop-app-macos).

### Terminal (macOS / Linux)

Requires Go 1.26.1+, `just`, and a C compiler.

```bash
just install
obey-voice doctor --voice-devices
obey-voice
```

Put your Go binary directory on `PATH` (`$GOBIN`, or `$(go env GOPATH)/bin`). On macOS, give your terminal microphone access. Choose a model in **Settings → Brain** before starting a conversation. [Full install guide](docs/install.md).

## Group chat

Give each persona a voice, model, and prompt. Personas using different brain providers can share the same conversation. Say “Jarvis, what do you think?” to bring one in; the last speaker handles follow-ups. Agents can hand questions to each other, and you can interrupt at any time. [Set up personas](docs/personas.md).

## Explore

- [Usage](docs/usage.md): text input, phone access, and terminal controls.
- [Voice stack](docs/voice-stack.md): pipeline diagram and speech models.
- [Benchmarks](docs/benchmarks/README.md) and [conversation scenarios](tests/convoflow/README.md): compare models, latency, and routing.
- [Pipeline debugging](docs/pipeline-debugging.md): inspect live turns or replay recorded conversations.
- [All docs](docs/README.md): meeting recording, narration, scenes, configuration, and development.

Built with [Festival](https://github.com/Obedience-Corp/festival), the planning and execution system I use to build with agents. You don't need Festival to run Obey Voice.

[Contributing](CONTRIBUTING.md) · [Issues](https://github.com/Obedience-Corp/obey-voice/issues) · [AGPL-3.0-only license](LICENSE) · [Contributor agreement](CLA.md) · [Notice](NOTICE)

---

<p align="center">
  Built by <a href="https://obediencecorp.com">Obedience Corp</a> ·
  <a href="https://github.com/Obedience-Corp">GitHub</a>
</p>
