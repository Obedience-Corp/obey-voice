# Experimental native audio integration (WI-43b072)

Native-aware CLI, terminal and desktop conversations now opt into the prepared
persona integration. The generic binding API remains guarded for unaware hosts.
[Frontdoor verification](frontdoor.md) covers actual startup/default selection,
restore/projection, native UI and the numeric-integrity correction.
[Prepared bindings](activation.md) describes the transaction machinery. Existing
Standard personas retain their providers and saved settings. Replay UX, broader
acceptance/quality and exact runtime context accounting remain open.

The implementation shares the existing capture/VAD/endpoint/completion owner,
with a decoder-free `utterance.Provider`. Native input and image attachments have
separate validation. Ollama receives verified PCM16 WAV through its audio-capable
Images wire field. Required ASR and speech use that same selected model/endpoint.
No external recognizer/synthesizer, microphone, tools or audible device is used by
the fixture checks.

Native delivery displays text while buffering at most 256 KiB until successful
chat/tool completion. Failed partial replies do not synthesize. HTTP inference
ownership lasts until body Close; optional captions yield to real new input and
typed requests, while merely listening may continue alongside a caption. Canceled
turns never launch a new caption. Updates address the original utterance/capture
epoch and do not create a user or assistant turn.

A 32 MiB session media budget and explicit ownership keep input available for
history/captions. Recording retention is opt-in. Only canonically admitted input
publishes a private retained WAV, before session autosave can reference it.
Standard saves remain v2; audio evidence requires v3. Ready transcripts may project
recordings into text context; typed accompaniment alone cannot replace missing
audio. Audio-only context cannot be silently evicted or compacted as blank text.

## Verification

- `just test native-audio`: focused race suites; no live model, device or download.
- `just test unit`, `just test voiceflow`, `just build`: whole-project gates.
- `OBEY_NATIVE_AUDIO_FIXTURE="$PWD/pkg/voiceagent/nativeaudio/assets/setup.wav" go test ./pkg/voiceagent/nativeaudio -run '^TestLiveNativeProductionAdapters$' -count=1 -v`: explicitly selected installed local Ollama model, isolated temporary config and software drain. This checks adapters/scheduler, not complete profile activation or model quality. It does not install a model.

The historical foundation checkpoint was tested from an exact archive, excluding
then-ongoing candidate setup work. Current host checks are in [frontdoor.md](frontdoor.md). Native race suites, build, voiceflow and audio-crackle pass.
The first whole-unit run reported 5,457/5,459 passing: the scene dependency guard
failed and the TUI dashboard reported one unnamed failure. Raw TUI reruns pass;
the initial failure is retained rather than described as a clean full-unit run.
The confirmed scene regression was repaired by moving the shared WAV metadata
reader into a dependency-free package, preserving the existing audio API. Scene,
audio and chatmedia race suites then pass, as does changed-code lint.

That foundation full-local-lint run reported unchanged errcheck findings in
`job/table_test.go:670` and `tts/qwen/native_session.go:220,223` (also present in
main). The review correction separately passed the full pipeline race suite and
changed-code lint. These results do not qualify activation or model quality.

The diagnostic context probe accepts an explicit fresh output path:

```sh
python3 docs/native-audio/evidence/context_accounting_probe.py --output /tmp/native-context-proof.json
```

The [ordinary agent proof](ordinary-agent-proof.md) now covers two actual recorded
inputs through constructor, selector/history and software speech, with verified
zero forbidden-factory execution. Current production UI integration evidence is separate in [frontdoor.md](frontdoor.md).

## Evidence and open gates

[evidence](evidence/) preserves actual receipts, including failures. Local machine
paths are redacted; measurements/hashes and synthetic generated replies remain
unchanged. The fixture contains no user recording or project data; the stock
persona-example claims stay visible for independent qualitative review. The synthetic
fixture's canonical WAV is under `pkg/voiceagent/nativeaudio/assets/`; its provenance
and both raw/canonical hashes are documented there.

- New ASR adapter returned the exact spoken question in 1,415 ms on Ollama
  0.40.12/model digest `19ff9eb6…`.
- Actual production persona/history adapters completed audio chat then same-model
  speech to software drain. Their reply included unrequested project-action
  claims after the correct arithmetic answer, copied from persona examples.
  Both initial and one controlled guidance trial remain failed meaning/claim
  quality evidence. The old prompt-replacing proof proxy is not production code.
- The context probe found `num_ctx:2048` did not bound the tested MLX request:
  text input reported 4,141 prompt tokens; audio reported 4,172. Both audio
  `truncate:true` and `false` returned the same count. The receipt records request
  settings, source identity and statuses without base64 payloads. This does not
  establish a safe context budget.
- The [model card](https://ollama.com/library/lfm3-audio:2b) states 32K context;
  installed `/api/show` reports 128000. The [processor configuration](https://ollama.com/library/lfm3-audio:2b/blobs/c5cbf344413a)
  specifies hop 160 and encoder stride 8. Neither a text-only estimate nor one
  text/audio token difference qualifies exact audio history accounting.

The context-budget discrepancy remains an open no-omission/resource-safety
investigation. A20 specifically covers unavailable text/image projection: typed
rejections now retain the previous binding; product Retry/New context actions
remain to be integrated. A24 covers typed/local/completion native speech and
scheduling, not model accuracy. A06 now has two distinct real production-path
fixture responses in the ordinary-agent proof. Native selection/startup rollback, restart/replay/deletion,
UI activation, fresh whole-project Standard checks and broader quality/physical
listener gates remain explicit. No complete A01–A25 pass is claimed.
