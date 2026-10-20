# Obey Voice Agent

A Rust/egui desktop workspace for Obey Voice. The macOS window uses native frosted glass, integrated window controls, and saved Light/Dark appearance. The app reads your existing Obey Voice personas, configuration, sessions, models, and library.

Windows bridge startup is currently disabled: native library output cannot yet be safely isolated from its JSONL protocol. macOS is the validated platform.

## Build and run

Requires Rust 1.95 (pinned by `rust-toolchain.toml`), Go 1.26.1+, a C compiler, and `just`. Authenticate Git access to the private Obedience-Corp dependencies first. Configure `GOPRIVATE` to include `github.com/Obedience-Corp/*` if it is not already configured.

```sh
just build
just run
```

The Go companion is built into `bin/obey-voice-engine`. The Rust UI finds it beside the desktop executable, then in the repository's `bin` directory. `OBEY_VOICE_ENGINE` can select an explicit companion path. The engine revision is pinned in `bridge/go.mod`; a sibling checkout is not required.

For macOS:

```sh
just bundle
just bundle-release  # optimized Rust build
```

Open `dist/Obey Voice Agent.app`. Both commands replace this local bundle, include the companion and native speech libraries, and verify its ad-hoc signature. They do not install or publish the app. Public distribution would require Developer ID signing and notarization.

## Development commands

Run `just` for help, or `just --list --list-submodules` for every recipe. The root Justfile provides common entry points and delegates to focused modules under `.justfiles/`.

| Module | Commands | Purpose |
|--------|----------|---------|
| `dev` | `bridge`, `build`, `release`, `run`, `demo` | Go companion, debug/optimized Rust builds, local runs |
| `lint` | `all`, `justfiles`, `fmt-check`, `fmt`, `clippy`, `go-vet` | Read-only checks; `fmt` explicitly applies Rust formatting |
| `test` | `all`, `rust [filter]`, `bridge [pattern]`, `ui` | Automated tests and an opt-in interactive fixture |
| `dist` | `mac`, `mac-release`, `verify` | Local macOS packaging and signature verification |

`just check` is the complete unattended gate: Justfile/Rust formatting, Clippy, Go vet, Rust tests, Go race tests, and a debug build. It does not launch the app, open audio devices, or run hardware acceptance. `just test` retains its original behavior and runs both automated suites.

```sh
just test rust malformed_protocol
just test bridge 'TestProtocolOutput|TestDesktopTextRoundTrip'
just build-release
just dist verify
```

`just run` accepts app arguments, for example `just -- run --text-only --silent`. Runs use Cargo, and bundling reads Cargo metadata, so a configured `CARGO_TARGET_DIR` is respected. `just dev check` and `just dev test` remain available as compatibility entry points; the former retains its original lint-only behavior.

## Workflow

- **Home / Sessions:** start a conversation, choose a persona or saved default, resume history, or confirm deletion. Typed-only and silent modes are available in the start dialog.
- **Conversation:** streaming replies, optional thinking, activity, input/output levels, microphone pause, output mute, interrupt, hold-to-talk, recording, compaction, and slash commands. Enter sends; Shift+Enter adds a line. Ctrl+G pauses the mic, Ctrl+O mutes voice, Ctrl+T opens activity, and Ctrl+K toggles thinking. The composer uses native text editing; terminal Vim mode is not implemented.
- **Personas:** use the existing persona store, select a default, choose an in-chat responder, and create/edit/enable/disable/delete profiles. In-chat selection preserves the global default and finalized reply identity. Structured prompts retain their source-document editor.
- **Settings:** search the engine's configuration schema, edit defaults, inspect persona overrides, install/check models, run diagnostics, preview the saved voice, and register speakers from an open voice conversation.
- **Workers:** inspect/cancel tasks, control delivery, enable workers, and edit tier assignments, limits, and spending policy with revision checks.
- **Meeting:** record, pause, resume, annotate, bookmark, stop, review history, and file notes to a destination.
- **Library / Create audiobook:** select a Calibre book or an input file, choose narration options, and monitor or cancel rendering.
- **Use on another device:** run the existing remote server on Wi-Fi or Tailscale and display the address/pairing code. Leaving this screen stops sharing.

Managers retain the active conversation and pause voice admission. Leaving conversation/managers for another workflow saves and closes the chat. The launcher never opens the microphone. Model downloads and provider requirements follow the engine's existing configuration.

## Architecture

The desktop UI is Rust, using egui/eframe and wgpu (Metal on macOS). A supervised Go companion runs the **existing** Obey Voice conversation controller over private stdin/stdout JSONL. Speech, persona bindings, worker policy, meeting persistence, and session stores remain owned by Obey Voice. Local conversations do not start an HTTP server.

The exported runtime adapter is introduced by [obey-voice #227](https://github.com/Obedience-Corp/obey-voice/pull/227). The pinned revision can be built before that PR is merged.

## Verification and previews

```sh
just check       # lint, automated tests, and debug build
just test        # Rust tests and isolated Go bridge tests with the race detector
just demo        # sample data only; does not read your personas or use the mic
just ui-fixture  # builds an isolated test app and runs a mock local model server
```

`just demo` is only a visual preview. Use `just run` or the normal app bundle to load your actual personas. The UI fixture prints a separate test app path; open that app, choose application defaults, typed-only, and silent output, then exercise a conversation. Fixture state stays under the printed temporary directory. Stop the fixture server when finished.

`just test ui` and `just ui-fixture` are equivalent macOS-only, interactive commands. They build the debug bundle and keep the mock server running until Ctrl+C. Real microphone/speaker and other hardware acceptance remain explicit manual checks; they are not part of `just check` or `just test`.

Automated tests cover typed response persistence/resume, unavailable actions, invalid session paths, stale worker policy writes, empty Go collections, and immutable reply identity. Native-window checks cover appearance, configured personas, and the text workflow. Live microphone, speech output, meeting diarization, remote-device pairing, and audiobook rendering still require hardware/provider acceptance on your setup.

### Debugging the desktop and voice engine

Use **Diagnostics** in the page footer to inspect recent events, filter by error or
request ID, and copy the log path or visible records. `just logs` follows the latest
run in a terminal, including app restarts (`just logs --demo` for previews).

On macOS, logs are in `~/Library/Logs/obey-voice-agent/`. Other platforms use
`$XDG_STATE_HOME/obey-voice-agent` (default `~/.local/state`) or
`%LOCALAPPDATA%/obey-voice-agent`. Set `OBEY_VOICE_LOG_DIR` to choose another directory.
Each run writes JSONL with epoch-millisecond timestamps, request IDs, bridge
generations, command durations, lifecycle events, engine stderr, state/activity
summaries, and Rust warnings/errors. `OBEY_VOICE_TRACE_UI=1` additionally records
eframe repaint scheduling for UI performance debugging.

Writes happen off the UI thread. Each run retains a current 5 MiB segment and one
previous segment; startup prunes older run files to keep eight before adding the
new run. Recent records remain visible even if the log file cannot be written.
The queue and viewer are bounded, and dropped records are counted. Protocol logs
omit conversation text, prompts, config values, and reply bodies. Engine stderr
is recorded as emitted, so review diagnostics before sharing them.

Meeting capture shows live partial/final transcripts, microphone activity, and a
separate note editor. Notes clear only after the engine acknowledges saving them.
Saved meetings have a campaign picker and an optional configured destination ID.
Delivery uses the existing engine router; failed receipts remain failures even
when the command exits successfully. The app includes standard Go, user, and
Homebrew executable directories for Finder launches. `OBEY_VOICE_TOOL_PATH`
overrides that search path for custom installations or isolated tests.

`just test events` builds an isolated protocol-replay app for startup failures,
live transcript/notes, and routing receipts. It never opens audio devices or loads
personal config. `just test routing` exercises the real engine router with a
temporary meeting/config and a fake camp importer, verifying the saved document
and failed-delivery receipt. The existing `just ui-fixture` remains the real engine
text-conversation fixture. Hardware/provider acceptance is still manual.
