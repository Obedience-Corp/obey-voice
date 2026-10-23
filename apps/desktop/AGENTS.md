# Obey Voice Agent

Rust/egui native desktop UI with a Go JSONL bridge to the existing Obey Voice runtime.
Keep speech, persona identity, sessions, worker policy, and meeting persistence owned by obey-voice.
Do not reimplement those systems in Rust. Keep blocking work off egui's UI thread.
Keep hand-maintained files at or below 500 physical lines, including comments and blanks. Split by responsibility; do not compress code to satisfy the limit. `just lint file-size` enforces this for tracked and unignored new text files; only generated dependency records (`Cargo.lock`, `bridge/go.sum`) are exempt.
Keep app state/initialization in `src/app.rs`, actions/events/window rendering in `src/app/`, workflow screens in `src/screens/`, subprocess/protocol code in `src/backend/`, and drawing/platform code in `src/theme/`.
Use `just logs` for persistent local diagnostics; `just test events` for isolated protocol UI replay and `just test routing` for disposable engine-to-camp routing checks.
Use `just check` for the complete unattended lint/test/debug-build gate. Use `just test rust <filter>` or `just test bridge <pattern>` for focused checks.
Keep recipes in the owning `.justfiles/` module (`dev`, `lint`, `test`, `dist`), with convenient root entry points.
Use `just bundle` or `just bundle-release` for locally signed macOS bundles; these do not install or publish.
Verify writes and conversation flow with the opt-in isolated `just ui-fixture` harness. Keep hardware acceptance out of automated gates.
`just demo` is visual-only sample data; the normal app must use the existing user config.
Never use live personal config for automated mutations or microphone tests.
Use `camp p commit` in this campaign. Open ready-for-review PRs; no AI attribution.
