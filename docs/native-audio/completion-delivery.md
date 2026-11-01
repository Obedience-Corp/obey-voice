# Native typed, local and worker-completion delivery

Typed input, local acknowledgements and worker announcements use the prepared persona's same-model speech provider. Typed input does not invoke recognition, local replies do not invoke chat, and worker settings remain independent of the foreground persona. The production-constructor fixture covers PrepareNativeCandidate → NewAgent → typed/local turns → RunBackgroundTurn, with a software output engine.

The audit reproduced a real omission: Ollama's private structured worker-completion request rendered only Turn.Content, so a retained recording with transcription off disappeared while its typed accompaniment remained. The new direct test first failed with zero recording payloads. Native completion now uses the existing verified audio resolver, bounded per-request projection loader and canonical persona/user projector. It sends the exact retained WAV in the existing audio Images field, without changing ordinary canonical/working history or its projection map. This preserves the existing private completion contract's bounded conversation excerpt (up to six turns), rather than creating another chat loop.

Missing original bytes use an actual Ready transcript, including both the transcript and typed instruction. Missing audio-only context returns the typed projection error before HTTP; it is not inferred as empty context or transcribed implicitly. Unsupported images and estimated context pressure also refuse before dispatch. Existing canonical-result fallback remains for genuine unavailable/incompatible generation, not as a substitute for integrating supported native audio. Native completion sends explicit truncate=false and shift=false. The context allowance remains an estimate, with the separately recorded runtime accounting discrepancy open.

Private completion stays tools-free. Historical tool/system/unknown-role content is quoted as user data; internal shared-context roles become valid wire roles. No historical entry can introduce new system authority or unmatched tool calls. The common verified loader also rejects a reused content hash with conflicting sample geometry.

## Verification

Code `9d447a4a` plus quoted-history follow-up `906b3236`; [receipt](evidence/native-delivery.json). Deterministic local HTTP and synthetic PCM, not model/listening qualification:

- Selected model/voice receive exactly quiet setup, typed answer, local acknowledgement and completion speech requests. ASR count is zero; observed maximum active HTTP bodies is one. Local reply adds no chat call. The completion request includes one byte-identical retained WAV and preserves worker/global configuration.
- Twelve speech samples reach the software stream sink. The result correctly remains Held without an audible delivery receipt; it is not marked communicated by pretending a software drain is physical playback.
- Direct race tests cover unavailable disabled/pending/failed transcripts, Ready transcript plus typed instruction, unsupported images, estimated pressure, conflicting cached geometry, valid wire roles and unchanged ordinary history/projection.
- Whole-unit run passes 5,540/5,540 tests in 86 packages. Native race suites pass; the final brain/factory race after the quoted-role follow-up passes 44.703/10.264 seconds. Changed-code lint, fixture voiceflow, software crackle and build (60.74 seconds) pass.

The prior live ordinary-agent proof establishes real recorded-input model transport separately. This fixture does not claim general response quality, exact tokenizer accounting, physical playback or new editor task qualification.
