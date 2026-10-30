# Quiet finite job evidence

WI-43b072, 2026-10-30, macOS Apple Silicon. Development CLI built with
`just build`; no microphone or device playback. Isolated root
`/tmp/wi-43b072-sol`, no global settings/model installation changes.

Input: licensed AMI ES2002a channel 0, 130–160 seconds, from the editor's
existing CC BY 4.0 fixture. [AMI corpus](https://groups.inf.ed.ac.uk/ami/corpus/).
Source movie SHA and decoded PCM SHA are recorded in the manifest. PCM has
480,000 samples at 16 kHz, source origin 2,080,000 samples. This narration task
validates the input identity but **does not condition speech on its content**.
Caller-approved script: “This is an explicit narration test. The source recording
stays unchanged.” Actual transport is `text_then_speech`.

Command: `bin/obey-voice transcribe audio-job --audio-config
/tmp/wi-43b072-sol/audio-config.json --request /tmp/wi-43b072-sol/request.json
--jsonl`. Exit 0. Ordered accepted/progress/artifact/completed events retained.
LFM3 Ollama 0.40.12 returned 130,560 mono samples at 24 kHz, 5.44 seconds.
The original range is 30 seconds: this difference must be reviewed before Apply;
no picture retiming or voice-conversion claim follows from the result. Generated
WAV remains under the isolated root; its SHA is in the retained manifest.

This proves the actual finite-job adapter's narration artifact path. It does not
pass E03 audio transformation, task quality, signed installer, UI/timeline,
project relocation or export acceptance. No subjective listening occurred.
