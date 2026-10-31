# Experimental Native conversation activation

The ordinary CLI, terminal conversation screen and desktop now use the same prepared Native startup/selection/restore boundary. Set per-persona Native choices in the persona form or CLI, check setup quietly, then select that persona. Native default selection persists identity without replacing global Standard provider/worker choices. The generic unaware-host API keeps its guard.

Startup checks the selected runtime/model/voice before capture. Native skips external recognizer and speech-asset preparation, uses finalized input over the existing capture/VAD, and routes chat/speech through its prepared same-model client. Text-only and no-voice starts remain device-free; silent persona replacements cannot install speech. Voice capture requires VAD. Unavailable setup returns to persona selection with the saved session/default intact; there is no Standard fallback.

Restoration verifies retained media in its session namespace. Native receives exact retained WAV bytes, while Standard requires an actual Ready recording transcript in addition to typed accompaniment. Unsupported audio/image projections fail before model construction or initial persistence. Missing context does not rewrite the saved session. Native Clear refreshes scope before the next capture policy snapshot. Caption producers join/materialize before publication leases retire; repeated cleanup remains safe.

The active Native badge follows published runtime selection, not a saved draft or speech-provider name. Input availability includes finalized Native providers, including passive completion ownership; pending worker speech waits for a capture yield. The following [recording-review slice](recording-review.md) adds original replay and explicit transcription retry; restoration alone did not prove those controls.

Context admission remains the [documented estimate](context-admission.md). It preserves complete working evidence and refuses pressure before dispatch; the runtime accounting/no-omission discrepancy is still open. Known model inaccuracy and unperformed-action claims remain experimental limitations. No general quality qualification is asserted.

## Verification

At host checkpoint 96ffc3ac: full native race suites pass (TUI 82.7 s/pipeline 48.3 s), command/app race pass 38.4/6.4 s, build passes 69.3 s, desktop check passes 41 Rust tests plus bridge race/lint/build, changed-code lint 0, and actual CGO-disabled Linux integration build passes. Product tests cover startup, typed turn, Native→Standard→Native, unchanged global choices, save/restart, Clear scope refresh, missing setup/context, exact retained WAV projection and Ready transcript projection. Supervisor overlays independently reproduce the restore boundaries.

The first full native race failed `TestChatMediaRejectedTerminal`: its 75ms scripted ticks raced asynchronous media work. A controlled 250ms validation reproduces the failure. Readiness-driven import/validation/reply polling preserves all assertions and passes all media terminal variants three times under race; the full native race subsequently passes. No blind longer sleep was introduced.

[Native UI/CLI receipt](evidence/frontdoor-ui.json): private fixture `obey-desktop-ui-d0ldedl6`, text-only/silent launcher, fake local protocol. CUA verified saved Native default, ready mode/model badge, disabled mic/speech controls, typed response, quit/reopen/Continue latest with the same ID/history and a second response. Plain CLI also completed and honored typed exit. Saved sessions are format 3; the actual resumed request includes prior host-attributed user/assistant history. Three starts produce three quiet setup speech calls, zero ASR and zero foreground speech. This proves integration/persistence, not real-model quality or hardware.

## Numeric integrity correction

The first UI fixture's bare `15` exposed a genuine production heuristic concern. A real installed Ollama 0.40.12/LFM3 trial with Samantha/default integrity confirmed it: explicit-stop `15` (two tokens) was incorrectly classified as mid-sentence. Recovery repeated 15 and ultimately added unwanted host-context prose to canonical history. This is preserved as a failed trial, not classified as fixture-only.

Commit 03e9445c accepts only explicitly stopped numeric scalars as complete without sentence punctuation. Length termination, missing stop, prompt saturation, malformed numbers and partial prose retain their safeguards. Full brain race and scoped lint pass. An exact clean-head real rerun of the same question completed with only the user turn and assistant `15`, zero truncation/recovery events, in 5.08 s. [Paired receipt](evidence/numeric-integrity.json). This small controlled trial does not establish general arithmetic or model quality.

No microphone, audible device, regular user configuration, shared service change or model download was used. Recorded-input product UI/replay, broader A24 fixture coverage, exact runtime context accounting and the remaining design acceptance audit stay open.
