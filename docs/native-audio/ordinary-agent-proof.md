# Ordinary native agent fixture proof

The real opt-in trial passed at committed source
`375913ebdafdd2491766eb7ea97d238e379f10a6` using installed Ollama 0.40.12,
LFM3 Audio digest `19ff9eb6e621de5643e27d7a3d64e3fe62f9cd22d1471c9a34c01fb69b8e4f74`.
This is the ordinary voice pipeline, not the earlier direct-adapter proof proxy.
The host uses actual persona resolution, prepared agent construction, native
selector admission, production Samantha prompt/history, shared speech and atomic
session saving. It injects finalized synthetic input and a software drain. It does
not test physical capture/VAD/pre-roll, audible playback, worker runtime or UI
activation. The worker is explicitly an unused fixture provider.

- [Actual request/result receipt](evidence/native-ordinary-agent.json).
- [Atomic Go coverage profile](evidence/native-ordinary-agent.cover).
- [Verified forbidden-factory counters](evidence/native-ordinary-agent-factories.json).

Addition and multiplication produced distinct actual replies, beginning with `15`
and `12`. Both also repeated the stock unrequested claim that a project was set up.
That known response-quality limitation remains visible; neither answer is described
as qualified accuracy or a default replacement. The written experimental opt-in
contract does not require general answer accuracy. Input is real synthetic WAV,
never a supplied transcript. The second actual request includes both canonical WAV
hashes in production history; every user wire text is empty.

The ordinary turns made two chat and two same-model speech requests, zero ASR
requests, and at most one active inference response body. The no-speech fixture made
zero HTTP requests. Speech PCM drained 82,175 and 84,480 samples at 24 kHz. Events
show chat completion before software-sink PCM readiness for each response. These are
software measurements, not microphone-to-speaker latency or listening quality.

The isolated external-model directory stayed empty. Atomic coverage verifies zero
executions of every instrumented statement block in the actual Standard STT,
Parakeet, Whisper/Sherpa/TranscribeCPP, Kokoro, Qwen and Phonon constructors. An
altered counter fixture is rejected by the verifier. No model is installed, no
microphone/speaker opens, and global Standard settings remain unchanged. Native
format-3 mode and two canonical audio-only turns reload; each retained recording
exists with its verified identity before autosave references it.

The pass-through client observer records hashes and request metadata, never changes
model, prompts or payloads, and preserves body ownership/idle-connection cleanup.
It starts after quiet setup, so request counts describe ordinary agent operation;
the setup result is recorded separately. Synthetic replies remain verbatim. No
user recording/project data, base64 WAV, full system prompt, credentials or local
machine paths occur in the published receipt.

Reproduce from a clean committed checkout using absolute fresh paths:

```sh
just test native-persona-proof /absolute/design/proof/artifacts /tmp/native-agent.json /tmp/native-agent.cover
```

The fixture directory must contain the documented `addition.wav` and
`multiplication.wav`; the test checks actual admitted hashes. The source tree must
be committed. The test uses isolated app/session/model settings, software drain
and the explicitly selected installed local runtime; it does not download assets.

Verify the published counters without inference:

```sh
go run ./docs/native-audio/probe/factory-coverage --profile docs/native-audio/evidence/native-ordinary-agent.cover --receipt docs/native-audio/evidence/native-ordinary-agent.json --out /tmp/native-factories.json
```

The verifier reads exact constructor source from the receipt commit using Git AST
ranges and rejects absent instrumentation or any executed forbidden block. The
profile/receipt/source hashes make the evidence reviewable after later code edits.
Production UI/startup/restart/replay integration and the remaining acceptance rows
remain open; this proof does not authorize a blanket complete A01–A25 claim.
