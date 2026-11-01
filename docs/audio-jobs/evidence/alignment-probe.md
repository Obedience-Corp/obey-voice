# Forced-alignment feasibility checkpoint

WI-43b072 / E08, 2026-10-30. Qwen3-ForcedAligner is a candidate, not an editor
dependency or qualified provider. The first real offline fixture returned seven
zero-duration words. Those results cannot pass the editor's positive-duration
word contract; do not pad them or present these timestamps as qualified cuts.

The official [Qwen model card](https://huggingface.co/Qwen/Qwen3-ForcedAligner-0.6B)
describes audio-plus-transcript alignment and an Apache-2.0 license. The
[reference implementation](https://github.com/QwenLM/Qwen3-ASR/blob/main/qwen_asr/inference/qwen3_forced_aligner.py)
predicts timestamp indices. The downloaded pinned config uses an 80 ms timestamp
step; this is model output quantization, not evidence of 80 ms accuracy.
The current sherpa-onnx C API was also inspected; no forced-alignment entry point
was found. This prototype therefore uses an isolated reference Python runtime,
without introducing Python into the product or adopting a deployment dependency.

## Exact trial

- Model revision: `c7cbfc2048c462b0d63a45797104fc9db3ad62b7`.
- Weight SHA-256: `47831d0e82f96b20e9034dba01a075ee06436654719f6a68289e49f1b65ce0e7`.
- qwen-asr 0.0.6, Torch 2.15.0, Transformers 4.57.6, CPU float32, four threads,
  eager attention, local-files-only loading and network-denied sandbox.
- Input: exactly the `initial-filler-0` PCM WAV from the
  [paired recognition comparison](recognition-comparison.md), SHA-256
  `d620c7237cebd353f6c0d54b6b8f176233e46f0fedec0a09c7bcdb9a1ca976de`.
- Text: the 29-word manual channel-A AMI reference. This is an oracle diagnostic,
  **not** recognizer output. AMI Consortium, CC BY 4.0; no private recording.

The aligner returned 29 words. The existing time-constrained diagnostic matcher
matched 28/29 within one second. For those matched words, 56 start/end errors
have median 60 ms and p95 450 ms. The single annotated filler contributes only
two boundary errors: median 55 ms and p95 100 ms. This tiny sample neither
qualifies the filler gate nor establishes population accuracy. The seven
zero-duration results remain visible in the raw report and disqualify this
particular word artifact independently of aggregate metrics. No overlaps were
returned. Words from other speakers and incomplete channel-A transcription
remain confounds in this meeting recording.

The first local smoke took about 5.2 seconds for alignment; the final repeated
probe took about 1.2 seconds. These are process-local observations, not a cold /
warm resource benchmark. No microphone, speaker, automatic model fetch or
background service was used. The model weights and configs were explicitly
downloaded first and checked against the official pinned revision. The probe
rehashes all required model files before inference.

[Report](alignment-smoke-2026-10-30/report.json),
[model download receipt](alignment-smoke-2026-10-30/model-receipt.json), and
[isolated environment versions](alignment-smoke-2026-10-30/requirements.txt)
retain the evidence. The report includes the exact probe and reference hashes.
The model/environment themselves are private temporary test assets, not bundled
or committed product files.

## Reproduce and continue

Use an isolated Python 3.12 environment with `qwen-asr==0.0.6`, and explicitly
download the pinned model revision to a local directory. This trial's full
resolved environment is recorded above. Run with network access denied:

```sh
sandbox-exec -f /absolute/deny-network.sb /absolute/venv/bin/python \
  docs/audio-jobs/probe/forced_aligner.py \
  --model /absolute/model-directory \
  --input /absolute/initial-filler-0/input.wav \
  --reference /absolute/initial-filler-0/reference.json \
  --output /absolute/new-report.json
```

Next executable work is to separate oracle alignment from alignment on actual
Parakeet/LFM3 hypotheses over the same fixed excerpts, preserving invalid and
unmatched words. Compare a finer acoustic alignment path if these failures
persist. No inferred timestamps, padded durations or confidence fabricated from
model logits may replace the measured gates. Recognition omissions still need a
verbatim-sensitive recognizer; alignment cannot recover missing transcript words.
