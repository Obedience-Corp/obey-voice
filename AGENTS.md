# Obey Voice Agent

Rust/egui native desktop UI with a Go JSONL bridge to the existing Obey Voice runtime.
Keep speech, persona identity, sessions, worker policy, and meeting persistence owned by obey-voice.
Do not reimplement those systems in Rust. Keep blocking work off egui's UI thread.
Use `just check`, `just test`, and `just build`; verify writes and conversation flow with the isolated `just ui-fixture` harness.
`just demo` is visual-only sample data; the normal app must use the existing user config.
Never use live personal config for automated mutations or microphone tests.
Use `camp p commit` in this campaign. Open ready-for-review PRs; no AI attribution.
