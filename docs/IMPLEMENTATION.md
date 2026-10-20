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
- macOS was exercised. Other platforms and signed/notarized distribution were not validated.

Next executable acceptance: `just run`, start a voice conversation with a configured persona,
exercise microphone pause/hold-to-talk/interrupt, then record and review a short meeting.
Use `just ui-fixture` for any automated write checks; never create test personas or history in the personal install.
