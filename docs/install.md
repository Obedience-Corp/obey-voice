# Install

[Back to README](../README.md)

Release reviewers: use [first-conversation acceptance](first-conversation-acceptance.md)
for isolated setup checks and the separate clean-machine audio procedure.

## Requirements

- Go 1.26+ for source builds
- [just](https://github.com/casey/just) for source builds
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

Release packaging produces a relocatable `obey-voice-VERSION-linux-amd64.tar.gz` for
x86_64 glibc Linux. Check the [releases page](https://github.com/Obedience-Corp/obey-voice/releases)
for available archives.
Once downloaded, the archive contains the Obey Voice executable and its sherpa-onnx /
onnxruntime shared libraries; keep the executable and `lib/` directory
together.

```bash
# Download the matching .tar.gz.sha256 alongside the archive; substitute VERSION.
sha256sum --check obey-voice-VERSION-linux-amd64.tar.gz.sha256
tar -xzf obey-voice-VERSION-linux-amd64.tar.gz
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

## Homebrew (macOS / Linux)

The `obedience-corp/tap/obey-voice` formula installs the released CLI with its
native speech libraries. It supports Apple Silicon macOS 15+ and x86_64 Linux
with glibc 2.39+ (Ubuntu 24.04+). It does not install the desktop app; use the
[desktop source install](#desktop-app-macos) for that.

The separate desktop cask, `obedience-corp/tap/obey-voice-app`, is created by the
release pipeline after the first signed and notarized desktop archive is
published. It is **not yet available**. Once that first app release is public:

```bash
brew install --cask obedience-corp/tap/obey-voice-app
open "/Applications/Obey Voice Agent.app"
```

The cask includes its own engine and native libraries; it does not require the
CLI formula. Both share existing user settings and model caches. The cask
supports Apple Silicon macOS 15+. Use `brew upgrade --cask
obedience-corp/tap/obey-voice-app` for subsequent app releases; quit the app first.

**Availability:** the initial v0.3.0 bottles are still awaiting publication.
Use [the source install](#from-source) until those bottles are public. The
Homebrew commands for the published release are:

```bash
brew install obedience-corp/tap/obey-voice
obey-voice
```

If Homebrew asks you to trust the organization's tap, run
`brew trust --tap obedience-corp/tap`, then retry the install.
Choose a brain provider and download speech models during first-launch setup.
Homebrew does not install brain providers, download models, or start services.
For a compiler-free speech setup, select Parakeet STT and Kokoro TTS; native
Phonon on Linux needs a separate source toolchain. You can also prepare models
and check audio devices explicitly:

```bash
obey-voice models ensure
obey-voice doctor --voice-devices
```

Upgrade the released CLI with:

```bash
brew update
brew upgrade obedience-corp/tap/obey-voice
```

Settings, personas, sessions, and model caches survive upgrades and uninstall.
Homebrew and `just install` keep separate executables. If you use both, run
`type -a obey-voice` to see which command comes first on `PATH`; put your Go
binary directory first when developing. `brew upgrade` updates the released
copy, while `just install` rebuilds your current checkout. The older
`lancekrogers/tap/samantha` package remains independent.

Grant your terminal microphone access under System Settings →
Privacy & Security → Microphone on macOS.

## From source

```bash
just install    # Build this checkout, bundle native libraries, install, then ensure models
```

`just install` is also the development install: rerun it after editing code, even
with uncommitted changes. It builds your checkout locally and bundles the native
libraries selected by its Go dependencies. No release, tag, Homebrew formula,
or published Obey Voice binary is needed. The version includes the Git revision
and `-dirty` when applicable; a `v0.3.0-...` prefix is Git's nearest version tag,
not a dependency on the v0.3.0 release.

The executable and libraries live together under `$GOBIN/.obey-voice-dev/`
(default: `$(go env GOPATH)/bin/.obey-voice-dev/`). The `obey-voice` and `samantha`
commands point there. Installation verifies the new payload before replacing
the command and removes the previous dev payload after switching. Existing
release or Homebrew symlink targets are left intact. The installed dev build
continues to work if the source checkout or Go cache is removed or unmounted.
Go still needs an accessible module/build cache when compiling the next build.

Supported install hosts are Apple Silicon macOS and x86_64 glibc Linux. In
addition to Go, a C compiler, and `just`, install Python 3.12+, `curl`, and Bash
4+. macOS needs Xcode Command Line Tools (`otool`, `install_name_tool`, and
`codesign`); Linux needs `patchelf` and binutils. The shared native packaging
step retrieves checksum-pinned dependency license notices from upstream.

For the usual edit/reinstall loop with your existing models:

```bash
OBEY_VOICE_SKIP_MODELS_ENSURE=1 just install
obey voice --version    # Or: obey-voice --version
```

For running directly from the checkout (requires its Go cache):

```bash
just build
just run -- --text
```

Set `OBEY_VOICE_SKIP_MODELS_ENSURE=1` to skip model setup; existing settings and
model caches are reused. Qwen3-TTS is opt-in (`obey-voice models ensure --tts`): linux/amd64
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
