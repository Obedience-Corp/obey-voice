# Standard controls and reversible persona switching

Standard controls now have a production-host fixture covering spoken address, unknown persona refusal, ordinary recorded conversation, independent background dispatch, status and cancellation. The selected Native model receives the original WAV for the conversational request; controls use its required ASR route. Counters are seven ASR calls (including setup), one Native foreground chat and one explicitly selected independent worker request. No microphone or external search runs.

That test exposed a real asynchronous session-save race: checkpoint admission read the mutable session ID while Store.Save replaced the session value. Checkpoints now capture immutable identity before observer publication. A deterministic concurrency regression passes with the repair and reports DATA RACE with the old read restored. An existing snapshot test also now restores its deliberately altered identity before teardown; its mixed-owner rejection assertion remains intact.

## Native desktop evidence

[Structured receipt](evidence/controls-switching.json). An isolated software-only native app resumed a synthetic retained recording with transcription off. Saving Standard and selecting it correctly refused audio-only history, but the first UI pass exposed hidden feedback: the backend retained Native while the manager showed no actionable reason. Persona selection feedback now crosses the desktop wire and says “Switch blocked: transcribe recorded input or start a new conversation.” The rebuilt app verified that message and the unchanged Native binding.

Explicit transcription repaired the original row. Selecting Standard then showed its badge, used desktop-test, and returned the Standard fixture response with transcript text and no WAV. Switching back restored the saved Native endpoint, female voice, conversation controls, off policy and retention; its next request carried the byte-identical retained WAV and prior Standard response. Quit/reopen preserved Native and all six canonical turns. Saved Standard Juniper TTS/prompts and global configuration stayed byte-for-byte unchanged.

This is actual native UI and protocol/persistence evidence, not model quality or physical-device qualification. Three startup/reactivation quiet speech checks are separate from foreground output; explicit transcription ran once. The app and private server were closed after verification.

## Checks

Changed-package race checks pass for command, TUI, session and checkpoint packages; the command suite initially exposed the snapshot fixture cleanup and passed after correction. Focused desktop feedback and deterministic checkpoint tests pass. Scoped lint reports zero issues. Desktop check passes 41 Rust tests plus bridge checks; the development bundle builds. Initial lint contention was a parallel linter lock, not a code finding. Earlier HTTP fixture metadata/model expectations were corrected to match the production protocol rather than relaxing routing assertions.
