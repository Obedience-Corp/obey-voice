# Desktop implementation

Outcome: a native egui application following the Obey Voice launcher, chat, managers,
meeting, narration, library, and remote-access flows while sharing its runtime and data.

Architecture: a supervised Go child on private stdin/stdout JSONL. The child uses the
same conversation and meeting builders as the TUI. CLI management commands run as
separate child processes with argument arrays. No network listener for local chat.

Acceptance:
- New/resumed chats stream text, thinking, activity, levels and immutable persona identity.
- Mic pause, bounded talk, interrupt, output mute, compact, record, manager return, and shutdown work.
- Sessions and config/persona edits persist through the upstream stores.
- Worker controls operate on the live pipeline; meeting capture releases the microphone at stop.
- Native forms cover library/narration and managed remote sharing.
- Missing backend, command failure, stale replies and process exit remain recoverable.
- Rust checks, Go bridge tests, upstream runtime tests, and isolated real-window exercise pass.

Delivered:
- Private Obedience-Corp/obey-voice-agent repository registered with `camp project add`.
- Rust/egui/wgpu interface; macOS native backdrop, integrated traffic lights, saved Light/Dark appearance.
- Existing config/personas loaded by the normal app; sample data only with explicit demo mode.
- Pinned Go adapter from obey-voice #227; no sibling checkout needed to build.
- Native chat, managers, settings, enrollment, workers/policy, meetings, library/rendering and remote controls.
- macOS bundle includes speech dylibs with relative rpaths and local ad-hoc signing.

Verification (2026-10-19):
- `just check`: fmt, Clippy with warnings denied, Go vet passed.
- `just test`: Rust tests and Go race-detector tests passed, including persisted chat resume and worker-policy revision conflict.
- Upstream `go test ./...` and `just build` passed. An existing voice retry timing test failed once under concurrent load, then passed in the final full suite.
- Computer Use: Home, Conversation and Personas inspected in native light/dark glass windows; dark choice survived restart. The normal app showed all 10 existing configured personas and Samantha as default.
- Isolated real-window exercise: text-only/silent start, native composer Enter, assistant reply, complete /help output, manager return with original session, save, resume, and new persona creation all passed. Confirmed both turns and the new persona on disk under the temporary fixture root.

Remaining acceptance uncertainty:
- No live microphone/speaker, speaker-enrollment capture, real meeting diarization, remote-device pairing, or audiobook render was run. Their controls delegate to the existing engine; hardware/provider acceptance remains necessary.
- Native text editing uses platform editing shortcuts; terminal Vim-mode editing is not implemented in egui.
- Structured prompt documents are preserved; their source document remains the editing surface.
- macOS was exercised. Windows bridge startup now fails closed because native stdout cannot yet be isolated; other platforms and signed/notarized distribution were not validated.

Next executable acceptance: `just run`, start a voice conversation with a configured persona,
exercise microphone pause/hold-to-talk/interrupt, then record and review a short meeting.
Use `just ui-fixture` for any automated write checks; never create test personas or history in the personal install.

PR review follow-up (2026-10-19):
- Runtime #227: Settings uses the same app-default scope as config get/set, captures its entry fingerprint before edits, and refreshes the preview snapshot after a successful save. Session persona config stays bound to the conversation. A rapid Settings return retains pending voice cancellation until its completion.
- Runtime #227: Persist runs before bounded helper/turn draining. Queued turn commands release their drain reservation when shutdown drops them; running commands retain theirs until completion.
- Desktop #1: Invalid JSONL lines produce a warning while later snapshots continue. Only a reaped child (or failed spawn) reports engine loss. Reconnect cannot kill a live engine. Broken transport asks the child to stop and allows a 12-second grace period before forced termination.
- Desktop #1: Windows rejects bridge startup before capture; Unix keeps its private JSONL descriptor and redirects OS fd 1 to diagnostics.
- Regression coverage: actual controller startup cancellation, rapid Settings return with a different session voice, saved-settings refresh/reload, preview provider/device/rate isolation, dropped versus executing command drain ownership, process-level native stdout writes, malformed protocol recovery, live-engine reconnect protection, and exit ordering.
- Verification after fixes: upstream full Go suite and build passed; focused controller/command tests passed with the race detector. Desktop `just check`, `just test`, `just build`, bundle generation and strict signature verification passed against upstream `5fd002a85e55`. The Windows guard/test cross-compiled; it was not executed on Windows.
- Rebuilt native fixture: typed exchange, Settings return, save, resume, and app quit passed. Confirmed both turns on disk and no remaining fixture engine process. Live audio hardware remains untested.

Modular automation follow-up (2026-10-20):
- Root `just` now lists documented commands. `dev`, `lint`, `test`, and `dist` modules own builds/runs, code checks, automated/interactive tests, and local macOS packaging respectively.
- `just check` runs the full unattended lint/test/debug-build gate. `just test` and the original root entry points remain available. The UI fixture is explicitly opt-in; hardware acceptance remains manual.
- Added optimized build/bundle commands and automatic bundle signature verification. Packaging remains local and ad-hoc signed; installation, publishing, and notarization are not performed.
- Run arguments pass through as positional arguments. Runs use Cargo, and packaging discovers its target directory through metadata.
- Validation: full `just check` passed. Command-routing probes passed for nested directories, quoted test filters, argument forwarding, default tests, failure propagation, release packaging, and fixture routing. Python syntax and Justfile formatting checks passed.
- `CARGO_TARGET_DIR="$PWD/.artifacts/justfile-target" just bundle-release` built the optimized app outside the default target directory and passed strict bundle signature verification.
