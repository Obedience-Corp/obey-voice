# Synthetic voiceflow controls

These recordings replace fixtures whose original recording/synthesis source was
undocumented. `utterance.wav` says “Hello Samantha.” and `barge_in.wav` says
“Okay.” Both are locally generated Kokoro `af_heart` at speed 0.95, canonical
mono 16 kHz PCM16. They contain no imported performer recording.

See [`docs/audio/public-fixtures.json`](../../../docs/audio/public-fixtures.json)
for source-pack/voice/model hashes, license links, sample counts and byte digests.
Run `just public-audio-fixtures /path/to/models` to regenerate with existing assets;
no personal config, network inference or audio device is used. Offline ASR matches
the documented words. `just test voiceflow` passes on the replacements.

The voiceflow suite uses fake STT/brain/TTS and a VAD replay harness. Its hard-coded
STT response is a protocol test, not a live recognition accuracy measurement.
