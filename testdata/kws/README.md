# Synthetic acoustic-name smoke fixtures

These are synthesized speech, not human microphone recordings. Generated on macOS
with `say -v VOICE -o FILE.wav --data-format=LEI16@16000 TEXT` (no playback).
The Kokoro clip used `go run ./cmd/golden-tts -models-dir DIR -voice af_heart
-speed 0.95 -text TEXT -out FILE.wav`, then ffmpeg to 16 kHz mono PCM16.

| File | Voice | Spoken text | Acoustic Obey expected |
|---|---|---|---|
| bare.wav | Samantha | Obey. | yes |
| positive.wav | Samantha | Hey Obey, tell me the weather. | yes |
| british.wav | Daniel | Obey, can you help me? | yes |
| kokoro.wav | Kokoro af_heart | Hey Obey, tell me the weather. | yes |
| verb.wav | Samantha | You must obey the rules. | yes (not an address) |
| mention.wav | Samantha | Tell me about Obey. | yes (not an address) |
| negative.wav | Samantha | Okay, tell me the weather. | no |
| other-keyword.wav | Samantha | I will love you forever. | no |
| confusable.wav | Samantha | Oh baby, what a beautiful day. | no |

The accuracy benchmark adds deterministic noise at 10 dB SNR and reports all
cases. The native smoke test checks stable controls plus silence, noise alone,
cancellation, and silence immediately after each positive.
Acoustic hits alone must never authorize a persona switch. Semantic intent is
covered separately by the shared router tests and optional live classifier replay.
