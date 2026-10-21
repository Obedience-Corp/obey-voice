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

Appearance and conversation-controls follow-up (2026-10-20):
- Theme changes now discard the partially drawn egui pass and rebuild the root UI with the selected style before presenting it. This avoids mixing new surface colors with the previous sidebar, card, and text styles. Native appearance and saved preference remain synchronized with the selection.
- Conversation setup uses positive "Microphone input" and "Spoken replies" checkboxes, checked by default. Turning them off maps to the existing text-only/silent runtime flags; explicit CLI flags still determine initial state.
- Verification: `just check` passed (fmt, Clippy, Go vet, six Rust tests, Go race tests, debug builds). The render-output regression exercises repeated switches in both directions and checks that only the selected palette is emitted for the test panels.
- Rebuilt isolated native demo: both theme directions, checked audio defaults, independent toggling, and saved dark appearance after restart passed. No live audio hardware was exercised.

Desktop usability/debugging follow-up (2026-10-20):
- Found the theme latency in egui's theme-specific TextOptions: each toggle invalidated the SFNS glyph atlas. Keeping text rasterization options identical preserved the atlas. Native tracing measured apply-to-frame-end at ~970 ms before and 16 ms in both directions after the fix. Added a regression asserting no font-texture upload on warm theme switches. Preference writes use eframe autosave/exit; duplicate automatic native theme synchronization is disabled.
- Replaced the conversation floating window/title strip with a centered styled modal, grouped controls, primary action, and Escape/backdrop dismissal.
- Added bounded background JSONL diagnostics, startup/shutdown/panic and Rust warning/error capture, bridge request/reply and state/activity summaries, command timings/stderr, file-error visibility, footer viewer/filter/copy, and `just logs`. Protocol bodies are excluded; native/backend stderr is retained. Optional eframe trace logging supported the latency investigation.
- Recording workspace now renders phase, level, partial and committed transcripts beside notes. Successful note acknowledgements clear the editor and append to the visible notes; failures preserve the draft. Pause state follows acknowledgement.
- Campaign discovery uses an explicit executable search path suitable for Finder. Added a destination picker, bounded discovery, and authoritative routing-receipt handling; exit-0 failed exports no longer say Completed.
- Verification: native isolated replay showed the new modal, streamed partial/final transcript, note acknowledgement, retained failed-note draft, pause/resume, and startup failure in Diagnostics. Persistent fixture logs contain matching request IDs and engine stderr. Real engine routing against a temporary importer verified campaign working directory and saved note/transcript content, plus rejected delivery with exit 0. No personal meeting was routed and no live audio hardware was opened.
- Final checks: `just check` passed with 11 Rust tests, Go race tests, formatting/Clippy/vet, and debug builds; `just bundle` passed strict signature verification. The rebuilt native replay also verified campaign selection, visible rejected delivery, successful retry, and the saved destination document. Both modal themes and the terminal log reader were inspected.

Maintainability follow-up (2026-10-20):
- Split application setup/state, actions, event completion, preview data, and window rendering into `app.rs` plus `app/` modules. Split screens by workflow, backend execution/protocol/process handling, and theme drawing/platform integration. Keep shared controls and public entry points in their parent modules.
- Added `just lint file-size` to the full gate: all tracked/unignored hand-maintained text files must be at most 500 physical lines. Generated Cargo.lock and bridge/go.sum are the only exemptions. Verified 500-line acceptance and 501-line rejection on a disposable untracked file.
- Verified all 75 moved function bodies match the original after ignoring formatting. `just check` passed (11 Rust tests, Go race tests, formatting/Clippy/vet, builds). Rebuilt and signature-verified the isolated replay app; native UI exercised theme/dialog dispatch, provider failure, live partial/final transcript, successful note acknowledgement, and stop/review. No personal config or live audio was used.

Branding and settings organization follow-up (2026-10-20):
- Reused the native Obey Voice app’s exact Obedience Corp icon renditions, documented provenance, compiled a full ICNS into each bundle, and set the same logo as eframe’s runtime icon. Finder Get Info displayed the corporate logo; ICNS extraction confirmed all ten renditions.
- Settings now follow the TUI section order with distinct Workers, Qwen, STT, Audio, and Meeting sections. Provider model selection follows the saved brain provider. Extra engine configuration stays accessible in Remote/Models/Advanced and global search; Claude session caps/warnings are grouped under Advanced with a provider-specific explanation. Field editing still uses the shared engine schema and config commands.
- Verified real-engine isolated settings UI: Brain provider/model order, no Claude limits in normal Brain, global search locating the cap under Advanced, saved value 65000 on disk, reset removing the override, and Audio containing microphone/speaker/volume. Tests cover section routing, selected-provider filtering, hidden-key exclusion, and search reachability. No personal config or live audio devices were used.
- Final gates: `just check` passed with 13 Rust tests plus Go race tests, lint, file-size checks, and debug builds. `just bundle` passed strict code-signature verification.
