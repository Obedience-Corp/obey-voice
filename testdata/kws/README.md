# Synthetic acoustic-name smoke fixtures

These are locally synthesized Kokoro controls, not microphone recordings. The
former macOS Samantha/Daniel recordings have been replaced. Generation inputs,
voices, pinned source-pack hashes, sample counts and current WAV digests are in
[`docs/audio/public-fixtures.json`](../../docs/audio/public-fixtures.json).
The existing `kokoro.wav` is retained with its original documented command.

Regenerate the replacements with an existing standalone multi-lang pack:

```sh
just public-audio-fixtures /path/to/models
```

This uses explicit model paths, no personal configuration, download or playback.
It produces canonical mono 16 kHz PCM16. The pack is Apache-2.0; see the manifest's
source and license links. Generated-output terms were reviewed separately from
model redistribution obligations. British `bm_george` preserves an accent
control; other replacements use `af_heart` at speed 0.95.

| File | Voice | Spoken text | Acoustic Obey expected |
|---|---|---|---|
| bare.wav | Kokoro af_heart | Obey. | yes |
| positive.wav | Kokoro af_heart | Hey Obey, tell me the weather. | yes |
| british.wav | Kokoro bm_george | Obey, can you help me? | yes |
| kokoro.wav | Kokoro af_heart | Hey Obey, tell me the weather. | yes |
| verb.wav | Kokoro af_heart | You must obey the rules. | yes (not an address) |
| mention.wav | Kokoro af_heart | Tell me about Obey. | yes (not an address) |
| negative.wav | Kokoro af_heart | Okay, tell me the weather. | no |
| other-keyword.wav | Kokoro af_heart | I will love you forever. | no |
| confusable.wav | Kokoro af_heart | Oh baby, what a beautiful day. | no |

The accuracy benchmark adds deterministic noise at 10 dB SNR and reports all
cases. The native smoke test checks stable controls plus silence, noise alone,
cancellation, and silence immediately after each positive.
Acoustic hits alone must never authorize a persona switch. Semantic intent is
covered separately by the shared router tests and optional live classifier replay.
