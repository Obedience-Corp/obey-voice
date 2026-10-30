# Install

[Back to README](../README.md)

## Requirements

- Go 1.26+
- [just](https://github.com/casey/just)
- A C compiler for source builds (`gcc` or `clang`; Obey Voice uses CGO)
- A working microphone and speaker for voice mode
- Claude CLI on `PATH` when `brain_provider=claude`
- `cursor-agent` CLI on `PATH` when `brain_provider=cursor` ([Cursor Agent CLI](https://cursor.com/docs/cli/overview); auth via `CURSOR_API_KEY` or `cursor-agent login`)
- Grok CLI on `PATH` when `brain_provider=grok`
- Ollama running locally when `brain_provider=ollama`
- Docker or a compatible container runtime for integration tests

macOS users may need to grant microphone permission to the terminal app used to run Obey Voice.

## Desktop app (macOS)

The native desktop app is built from the same checkout as the CLI. Install
Rust 1.95 (selected by `apps/desktop/rust-toolchain.toml`), Go 1.26.1+, Python 3,
Xcode Command Line Tools, and `just`. The Go dependencies are public; no
private dependency access is required.

From the repository root:

```sh
just desktop install
open "$HOME/Applications/Obey Voice Agent.app"
```

`just desktop install` builds the release app, bundles its Go companion and
native speech libraries, verifies its ad-hoc signature, and installs
`~/Applications/Obey Voice Agent.app`. It does not launch the app or install
models. Run it again to update an existing install after quitting the app.
To install elsewhere, pass an existing or new directory you can write to:

```sh
just desktop install "/path/to/Applications"
```

The first-run guide checks your brain provider and walks through speech and
model setup. Existing settings, personas, and sessions in
`~/.obey/agents/voice/obey-voice` are reused, along with the model cache in
`~/.cache/obey-voice`. Allow the app microphone access when prompted for a voice
conversation. Typed-only and silent conversations are available from the start
dialog. By default, closing the window leaves the app in the menu bar; choose
**Quit Obey** to stop it before an update.

The bundle is signed locally for development use. Developer ID signing and
notarization are not part of this source-install recipe. The CLI's `just install`
remains separate. `just desktop bundle` and `just desktop bundle-release` only
create bundles in `apps/desktop/dist`; they do not install them.

[Desktop screenshots and usage](../apps/desktop/README.md) ·
[Desktop development](desktop-development.md)

## Linux amd64 archive

Release packaging produces a relocatable `obey-voice-linux-amd64.tar.gz` for
x86_64 glibc Linux. Check the [releases page](https://github.com/Obedience-Corp/obey-voice/releases)
for available archives.
Once downloaded, the archive contains the Obey Voice executable and its sherpa-onnx /
onnxruntime shared libraries; keep the executable and `lib/` directory
together.

```bash
tar -xzf obey-voice-linux-amd64.tar.gz
mkdir -p ~/.local/opt ~/.local/bin
mv obey-voice-linux-amd64 ~/.local/opt/obey-voice
ln -s ~/.local/opt/obey-voice/obey-voice ~/.local/bin/obey-voice
obey-voice --version
```

Ensure `~/.local/bin` is on `PATH`. Models are downloaded separately on first
setup:

```bash
obey-voice models ensure
obey-voice doctor --voice-devices
```

The release archive targets glibc Linux with Ubuntu 24.04 as its build and
compatibility floor. Source builds are also validated on Arch/EndeavourOS;
musl systems such as Alpine require a separate native build strategy.

## Homebrew (macOS)

The public `lancekrogers/tap/samantha` formula tracks the frozen public
`samantha` repo, not this engine. For Obey Voice, use the [source build](#from-source).
Grant your terminal microphone access under System Settings →
Privacy & Security → Microphone.

## From source

```bash
just install    # Build, sign on macOS when possible, install obey-voice to $GOBIN (+ a `samantha` alias symlink), then `models ensure` for the default STT/TTS/VAD stack
```

For development builds:

```bash
just build
just run -- --text
```

On Linux, `just install` is a source/developer install and retains the native
libraries supplied through the Go module cache. Use the release archive when
you need a relocatable installation that remains valid after cleaning that
cache. Set `OBEY_VOICE_SKIP_MODELS_ENSURE=1` to skip the model download (binary
only). Qwen3-TTS is opt-in (`obey-voice models ensure --tts`): linux/amd64
downloads the CPU package from `qwen3-tts-native` v0.1.1. It is slower than
Kokoro on a handheld; NVIDIA boxes should point `qwen_tts_native_url` at a
`linux-amd64-cuda` tarball when one is published. With
`tts_provider: openai-speech`, `obey-voice models ensure --tts` prepares the
selected native or external/Ollama runtime and Phonon's voice catalog; see
[OpenAI-compatible speech](openai-speech.md#install).

See [Development](development.md) for test, lint, and model-asset commands.

## Linux Phonon speech setup

Builtin Phonon personas use the managed native runtime on a fresh Linux setup.
The first `obey-voice models ensure --tts` verifies/builds the pinned native
server and prepares the model and named voices. On Arch, install the build
prerequisites with `sudo pacman -S --needed rust base-devel opus lame`; CUDA
also requires its toolkit and NVIDIA driver, and Vulkan requires its driver.
Existing explicit speech endpoints remain unchanged. For an existing local
Phonon/Ollama configuration on Linux, run `obey-voice config set openai_speech_runtime native` before
running setup. macOS keeps its Ollama path. See
[OpenAI-compatible speech](openai-speech.md#linux-managed-native-phonon) for
backend selection, external servers, cached operation and setup failures.
