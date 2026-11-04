# Quiet setup speech fixture

`setup.wav` is generated locally with Kokoro `af_heart`, speed 0.95. Text:
“What is seven plus eight? Reply with only the answer.” It replaces the macOS
Samantha recording; no microphone recording or model weights are bundled.

Current SHA-256, sample count, source pack/model/voice hashes and license links:
[`docs/audio/public-fixtures.json`](../../../../docs/audio/public-fixtures.json).
Reproduce with `just public-audio-fixtures /path/to/models`. Output is canonical
mono 16 kHz PCM16, with ancillary WAV chunks removed to match the setup/ASR
transport contract. Generation uses explicit paths and never opens an audio device.

Offline ASR recognized the intended addition prompt (numbers normalized to digits).
Native setup/cancellation/route tests pass with these new bytes. This is a finite
protocol-readiness fixture, not a speech-quality qualification or evidence that a
production model answered correctly. Earlier `docs/native-audio/evidence/` records
identify the former fixture by its original hash and measurements; those historical
results were not rewritten or relabeled as measurements of this replacement.
