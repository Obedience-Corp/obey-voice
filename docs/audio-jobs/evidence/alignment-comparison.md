# Oracle and recognizer alignment comparison

WI-43b072 / E08. Neither candidate qualifies the editor's timing or automatic-cut
requirements on this diagnostic. Qwen produces zero-duration words even with
manual reference text. Wav2Vec2 CTC produces positive, ordered intervals for
supported text, but its measured filler edges still exceed the existing gates.
Valid interval geometry is necessary; it does not establish timing accuracy.

This extends [the initial Qwen smoke](alignment-probe.md) across all 13 windows
from [the identical-input recognition comparison](recognition-comparison.md).
For each window, the aligner receives the same audio with three independent
text inputs: manual channel-A annotations (oracle), actual Parakeet output,
and actual LFM3 output. Empty text is recorded as `no_transcript`, not replaced
by another provider. No alignment result is admitted into the editor.

The [Qwen report](alignment-2026-10-30/qwen-report.json) and
[CTC report](alignment-2026-10-30/ctc-report.json) retain every word, input text,
invalid edge index, unmatched count, failed case, model/runtime fingerprint,
source offset and input/reference hash. The [Qwen log](alignment-2026-10-30/qwen-run.log)
and [CTC log](alignment-2026-10-30/ctc-run.log) retain warnings and case outcomes.
Sources are the AMI Meeting Corpus, credit AMI Consortium, CC BY 4.0. Raw audio
is reproduced from the existing comparison rather than stored again.

## Models and method

[Qwen3-ForcedAligner-0.6B](https://huggingface.co/Qwen/Qwen3-ForcedAligner-0.6B)
is the same pinned model and isolated reference environment as the smoke. Its
80 ms timestamp index step is recorded. Both models run CPU float32 with four
threads and external network access denied. Required local model files are
hashed against committed official-download receipts before loading.

[Wav2Vec2 Base 960h](https://huggingface.co/facebook/wav2vec2-base-960h) supplies
English CTC emissions from 16 kHz audio. Its Apache-2.0 model revision is
`22aad52d435eb6dbaf354bdad9b0da84ce7d6156`; weight SHA-256 is
`8aa76ab2243c81747a1f832954586bc566090c83a0ac167df6f31f0fa917d74a`.
The [model receipt](alignment-2026-10-30/ctc-model.json) records all downloaded
assets. Only safetensors weights are loaded. This is a proof environment, not a
new packaged editor dependency or a replacement recognizer.

The CTC prototype computes the maximum-probability complete path through a
blank-expanded supplied target. Repeated labels must be separated by a real
blank frame. Every target token must appear; unsupported characters and
impossible paths fail rather than dropping text. A bounded trellis and explicit
backtrace retain the full target. Three tests compare its scores with exhaustive
enumeration of small paths and verify repeat/impossible-input behavior.
The [PyTorch CTC alignment tutorial](https://docs.pytorch.org/audio/2.8.0/tutorials/ctc_forced_alignment_api_tutorial.html)
explains the emission/transcript approach; this prototype does not depend on
the tutorial's deprecated TorchAudio API.

CTC convolution geometry gives a 320-sample stride and 400-sample receptive
field. Frame-cell edges use a 40-sample origin, derived from the receptive-field
center minus half a stride. At 16 kHz that is a 20 ms step and 2.5 ms origin.
There is no fitted offset, word padding, duration repair or reference-based
boundary adjustment. Word intervals span the first through last character
frames; delimiter tokens are retained during alignment but excluded from word
edges. Path log probabilities are recorded as diagnostics, never confidence.

The CTC loader warns about an uninitialized `masked_spec_embed` training
parameter. The exact installed Transformers implementation was inspected:
without explicit mask indices, it uses that parameter only while training.
This run calls `eval()` and supplies no mask indices. The warning is retained;
no model training or checkpoint modification was performed.

## Results

Of 39 text/window combinations per model, nine have no transcript. Qwen measured
all 30 remaining combinations. CTC measured 26 and rejected four: one manual
annotation containing `_` and three LFM3 hypotheses containing unsupported
numerals. Those transcripts were not silently normalized into different words.

| Text input | Qwen measured / valid geometry | CTC measured / valid geometry | CTC failures |
|---|---:|---:|---:|
| Manual oracle | 8 / 0 | 7 / 7 | 1 |
| Parakeet hypothesis | 9 / 2 | 9 / 9 | 0 |
| LFM3 hypothesis | 13 / 2 | 10 / 10 | 3 |

Qwen returned 37 zero-duration edges across the oracle windows, 49 across
Parakeet-text windows, and 118 across LFM3-text windows. These counts concern
correlated diagnostic windows and are not population rates. All such words
remain in the reports; aggregate edge metrics do not hide their invalidity.

Oracle-text filler edge errors below compare matched start/end times with AMI
manual annotations. They isolate alignment from ASR omissions, while retaining
the meeting/cross-talk confound. Each row has only 2–12 matched filler edges.

| Window | Qwen median / p95 ms | CTC median / p95 ms |
|---|---:|---:|
| initial-filler / 0 | 55 / 100 | 285 / 397.5 |
| quiet-filler / 0 | 175 / 340 | 215 / 217.5 |
| meaningful-like-pauses / 0 | 310 / 990 | 300 / 1167.5 |
| meaningful-like-pauses / 1 | 125 / 230 | 330 / 377.5 |
| heldout-introduction / 0 | 260 / 500 | 270 / 397.5 |
| heldout-repeats / 0 | 115 / 830 | 82.5 / 1007.5 |
| heldout-repeats / 1 | 165 / 1360 | rejected: unsupported annotation character |
| heldout-repeats / 2 | 220 / 390 | 310 / 467.5 |

Every measured oracle row exceeds the existing 40 ms median filler-edge gate.
Most also exceed the 100 ms p95 gate. This is failure evidence, not a reason to
relax those gates. Alignment on recognizer hypotheses cannot recover omitted
fillers; corresponding matched counts and errors are in the reports.

The matcher only includes equal tokens whose starts are within one second;
unmatched reference words remain explicit and do not become zero error. Three
additional tests cover zero-duration output, dropped repeats and distant
matches. Metrics for an invalid word artifact are diagnostic only.

No independent listener/join adjudication, larger speaker-disjoint qualification,
language qualification or controlled cold/warm resource benchmark was performed.
Channel-A annotations omit cross-talk; empty channel-A reference is not proof
of silence. Windows overlap and the quiet case is a variant of another clip.
These limitations prevent broad model rankings or population safety claims.

## Reproduce and next work

Use the isolated Python environment recorded in the smoke, both explicitly
downloaded pinned models, and the full comparison directory containing WAVs
from `just test audio-recognition`. Each output directory must be new:

```sh
just test audio-alignment /absolute/venv/bin/python qwen /absolute/qwen-model \
  /absolute/recognition-comparison /absolute/new-qwen-output /absolute/deny-network.sb
just test audio-alignment /absolute/venv/bin/python ctc /absolute/ctc-model \
  /absolute/recognition-comparison /absolute/new-ctc-output /absolute/deny-network.sb
```

The recipe runs six trellis/metric regressions before inference. Direct CLI
`--case` options select individual fixed excerpts. Both full batches completed
and their processes exited. The recipe and this evidence do not change any
product support, installation or qualification flags.

Measured alignment remains open. Further work needs a verbatim-sensitive
recognizer, explicit annotation/number normalization with provenance, and
alignment qualified against adjudicated speech before editor integration.
Keep recognition, interval validity, acoustic timing accuracy and edited-join
acceptance as separate checks; a complete path cannot establish all four.
