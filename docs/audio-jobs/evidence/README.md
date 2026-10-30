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

## Recorded reproduction trials (failed conversion qualification)

Primary runtime references: [LiquidAI GGUF model card](https://huggingface.co/LiquidAI/LFM3-Audio-2B-GGUF/tree/02355c1a141e464a064f2ab2c276638dbcb7f38a),
[llama.cpp b11960 realtime protocol](https://github.com/ggml-org/llama.cpp/blob/8642541b2/tools/server/README.md#realtime-conversation).
The isolated model/projector files were explicitly downloaded into the proof
root, SHA verified, and served by an owned b11960 process on127.0.0.1:18143.
No global runtime, model, service or config was changed. Receipts pin hashes.

- Ollama `chat_then_speech`: requested every recorded word without summarizing,
  then synthesized the resulting words with the same model. Output20.4s for30s
  input (−32%); not preserved recording timing or direct voice conversion.
- llama.cpp `direct_audio_to_audio`: same recording with an explicit repeat all
  words/pauses/duration instruction, acknowledged by session.updated. Initial
  trial answered “A woman is speaking and then a young woman responds.” and
  produced3.52s. The structured rerun/terminal status and observed formats are
  retained separately in direct-receipt.json; neither trial qualifies conversion.
- One same-GGUF text-only ASR control with documented “Perform ASR in English.”
  returned the recording's spoken content (direct-asr-receipt.json), rather than
  the direct-output trial's summary. This distinguishes input recognition from
  failed recording reproduction; it does not establish exact word accuracy.

Generated WAVs and both staged/direct receipts are retained for independent
inspection. No listener, voice similarity, prosody, translation, multilingual,
word alignment or avatar quality pass is claimed. Conversion remains unavailable.
The probe harness checks pinned runtime/model/projector, nonempty/even PCM and
completed response status before writing the direct WAV. Reproduce from the
owning module with `AUDIO_JOB_PROOF_ROOT=/your/isolated/root go run
./docs/audio-jobs/probe/direct_probe.go` after staging the documented assets and
starting the owned server; it never opens a device or installs assets itself.
