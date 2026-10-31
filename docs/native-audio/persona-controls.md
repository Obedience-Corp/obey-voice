# Persona audio controls and quiet setup

Persona creation/editing accepts `--audio-mode standard|native`, `--audio-provider ollama`, `--audio-model`, `--audio-endpoint`, `--audio-voice`, `--audio-controls standard|conversation`, `--audio-transcript auto|required|off`, and `--retain-audio=true|false`. Omitted flags keep existing choices; old profiles retain their omitted audio object. Standard brain, speech and worker settings stay separate. Inactive native choices survive returning to Standard.

`obey-voice persona check-audio samantha --json` checks the saved native draft without activating capture or changing settings. The same audio flags can check unsaved changes. Quiet chat/speech output goes to a software drain. Conversation/off skips ASR; Standard controls require it. Protocol readiness and general quality qualification are separate fields. Setup does not install models or promise accurate answers. A failed check can be corrected and retried.

The desktop persona form exposes these choices and a cancellable quiet check; field changes, closing/replacing the form and engine loss retire stale checks. Standard audible preview is unavailable while the native form is selected. Native list cards identify experimental mode and disable Standard voice preview. The terminal form includes matching rows; `ctrl+g` runs/cancels setup. Any other form key, leaving the screen or shutdown cancels its check, and stale completion cannot replace current status.

Native drafts currently require `--no-activate` when created. Actual native-aware product startup/selector/restart adoption follows separately; these controls do not claim activation acceptance.

## Native desktop verification

Fixture-only protocol run in a private install layout, no model/device playback:

- Build/check/bundle passed before launch. Existing `scripts/ui-fixture.py --native-audio` creates a separately signed test bundle and private config; it never reads the user's config.
- Test app: `apps/desktop/dist/Obey Voice Test.app`; fixture root `/var/folders/9d/nyc358s50g7591g74wjx8pn40000gn/T/obey-desktop-ui-5ch0axzn`.
- CUA edited Samantha to Native, female voice, Conversation controls and transcription off. Quiet setup visibly passed; request log contains one `/api/chat`, one `/v1/audio/speech`, zero transcription requests. No audio device opened.
- Save/reopen retained all choices and preserved Standard openai-speech/Juniper. Save Standard/reopen confirmed Standard; selecting Native again recovered female/conversation/off. Final saved fixture profile is Standard with inactive native settings.
- App quit through UI and owned fixture server was reaped. This is actual form/persistence/quiet transport evidence, not real model inference, native conversation activation or physical listening proof.
- Subsequent list-card preview gating and terminal setup cleanup have automated coverage/checks; they were added after this native bundle and are not counted as observed native UI behavior.
