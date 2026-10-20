# Obey Voice Agent

A Rust/egui desktop workspace for Obey Voice. The macOS window uses native frosted glass, integrated window controls, and saved Light/Dark appearance. The app reads your existing Obey Voice personas, configuration, sessions, models, and library.

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
```

Open `dist/Obey Voice Agent.app`. The bundle includes the companion and its native speech libraries and is ad-hoc signed for local use. Public distribution would require Developer ID signing and notarization.

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
just check       # formatting, Clippy, Go vet
just test        # Rust tests and isolated Go bridge tests with the race detector
just demo        # sample data only; does not read your personas or use the mic
just ui-fixture  # builds an isolated test app and runs a mock local model server
```

`just demo` is only a visual preview. Use `just run` or the normal app bundle to load your actual personas. The UI fixture prints a separate test app path; open that app, choose application defaults, typed-only, and silent output, then exercise a conversation. Fixture state stays under the printed temporary directory. Stop the fixture server when finished.

Automated tests cover typed response persistence/resume, unavailable actions, invalid session paths, stale worker policy writes, empty Go collections, and immutable reply identity. Native-window checks cover appearance, configured personas, and the text workflow. Live microphone, speech output, meeting diarization, remote-device pairing, and audiobook rendering still require hardware/provider acceptance on your setup.
