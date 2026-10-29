# Desktop development

Obey Voice owns both the engine and the native desktop app. The engine's existing
Go module, CLI paths and release commands stay at the repository root. The Rust
app, Go JSONL companion, assets, fixtures and macOS packaging live in `apps/desktop`.

## Build and check

```sh
just desktop build
just desktop check
just desktop test bridge TestDesktopTextRoundTripPersistsAndResumes
just desktop run --text-only --silent
just desktop bundle
```

The desktop needs Rust 1.95 (pinned in its `rust-toolchain.toml`), the engine's Go
toolchain, a C compiler, and `just`. Linux also needs `libdbus-1-dev`,
`libxkbcommon-dev`, and `pkg-config`. Keep the existing private Go dependency access
configured. Root CLI builds do not require Rust.

You can also run the app's original commands from `apps/desktop`: `just check`,
`just build`, `just bundle`, and `just logs`. Build outputs stay under that directory
(or the configured Cargo target directory). Bundling is local and ad-hoc signed;
it does not install, publish or notarize the application.

## One engine source

`apps/desktop/bridge/go.mod` replaces the engine module with `../../..`. All bridge
builds—including direct Go commands, isolated UI fixtures, and builds with
`GOWORK=off`—therefore use the surrounding checkout. There is no independently
pinned/downloaded engine version and no required `go.work` file. The local lint
gate verifies this resolution and fails on an overriding workspace that points
elsewhere. Build from the full repository, not a copy of the desktop directory.

The bridge remains a small Go module; the Rust UI remains a separate Cargo
package. Engine state and persistence still belong to Go. Do not duplicate voice,
persona, session, meeting, or worker policy logic in Rust.

## Verification

Root `go test ./...` does not traverse the bridge's nested module. CI therefore
runs both the engine jobs and `just desktop check` on the existing Linux runner.
The desktop gate checks source resolution, formatting, file sizes, Clippy, Go vet,
Rust tests, bridge race tests and a debug build. Run it when changing the engine's
desktop adapter, CLI JSON, config schema, or desktop code.

For native acceptance, `just desktop ui-fixture` builds a separate test app with
temporary configuration and a mock local model. Verify a typed/silent exchange,
save, resume, and persisted state. `just desktop test work` prepares a protocol
replay for running/queued work. These fixtures never use personal voice config.
Real microphone/playback, live provider authentication and hardware acceptance
remain opt-in. See the [app guide](../apps/desktop/README.md) for other fixtures.

For persona preview acceptance, run `just test persona-preview` from
`apps/desktop`. In the test app, open Create persona or Edit and click Preview
voice. The isolated speech endpoint returns a quiet test tone and records voice
requests beside the temporary config directory. Voice `broken` returns an error;
`slow` delays synthesis for cancellation checks. Verify Preview/Stop, retry,
changing the draft, and closing the form before saving. Preview must leave the
config and persona files unchanged; saving must persist the selected draft voice.

## Repository migration

The initial import retains the original `Obedience-Corp/obey-voice-agent` history
as a merge parent at `8673e2cadc6f256ac74ed3294dc460321f881410`. Its files are normal
tracked files under `apps/desktop`, not a Git submodule. Pre-import history uses
the original root paths; inspect it with `git log 8673e2c -- src/`, for example.
Merge the migration PR with a **merge commit** to retain that history; squashing
would discard its ancestry from this repository's main branch.

New engine and desktop changes belong in one PR here. The original app repository
and its campaign checkout remain intact during the migration. Retiring that
checkout or archiving the old repository is a separate post-merge operation;
the independent `obey-voice-apple` application is outside this migration.
