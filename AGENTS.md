# Obey Voice Agent

Rust/egui native desktop UI with a Go JSONL bridge to the existing Obey Voice runtime.
Keep speech, persona identity, sessions, worker policy, and meeting persistence owned by obey-voice.
Do not reimplement those systems in Rust. Keep blocking work off egui's UI thread.
Use `just check` for the complete unattended lint/test/debug-build gate. Use `just test rust <filter>` or `just test bridge <pattern>` for focused checks.
Keep recipes in the owning `.justfiles/` module (`dev`, `lint`, `test`, `dist`), with convenient root entry points.
Use `just bundle` or `just bundle-release` for locally signed macOS bundles; these do not install or publish.
Verify writes and conversation flow with the opt-in isolated `just ui-fixture` harness. Keep hardware acceptance out of automated gates.
`just demo` is visual-only sample data; the normal app must use the existing user config.
Never use live personal config for automated mutations or microphone tests.
Use `camp p commit` in this campaign. Open ready-for-review PRs; no AI attribution.
