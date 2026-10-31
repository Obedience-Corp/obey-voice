# Identical-input LFM3 / Parakeet recognition comparison

WI-43b072 / E08, 2026-10-30. This is text-recognition evidence, not word-alignment
or automatic-cut qualification. On this diagnostic, Parakeet had lower word
edit rates in seven of eight windows with channel-A reference words and tied
in the eighth. Neither recognizer established verbatim filler preservation.
LFM3 remains useful as a reviewable text-finding provider; these results do not
justify using its text as measured word boundaries or promoting batch cleanup.

The [machine-readable report](recognition-2026-10-30/report.json) retains text,
normalized reference tokens, per-window metrics, model/runtime hashes, token
usage and worker hash. The [receipt](recognition-2026-10-30/receipt.json) records
the exact harness hashes, platform and loopback-only sandbox policy. Each window
directory retains requests, raw JSONL/stderr, reference annotations and native
findings/manifest. Large source WAVs are reproduced from the licensed corpus,
not copied into this repository.

## Method

Both providers receive exactly the same 16 kHz mono PCM16 WAV bytes for each
window. The six fixed excerpts, attenuation and calibration/held-out labels
come from [the existing word-timing diagnostic](../../word-timing/README.md).
They use the AMI Meeting Corpus, CC BY 4.0, credit AMI Consortium. This extends
that diagnostic; it does not replace its original timing measurements.

The full excerpts are decoded once, then split on the sample clock using the
native worker's 30-second / two-second-overlap plan. Each resulting window is
submitted independently to both providers. The native worker validates that
its actual input-window hash equals the WAV supplied to Parakeet. Each native
job starts and reaps its own managed runtime. There is no shared assistant,
microphone, speaker, model download or external network access. The process
inventory after completion contained no remaining llama-server.

LFM3 uses the production v2 transcription prompt, Q4_K_M model, Q8_0 projector
and pinned llama.cpp b11960 runtime from Voice #312 at `cf842c67`. Parakeet uses
the explicit TDT 0.6B v2 / sherpa-onnx 1.13.8 word provider. The report records
the actual model revisions/digests; configs and model selection are explicit.

Word edit rate is unit-cost Levenshtein distance divided by the reference word
count after the existing diagnostic's case/punctuation normalization. Repeated
words and fillers stay in the token sequence. Insertions can make the rate
exceed 100%. An empty reference has no defined rate; predicted word counts are
shown instead. Filler counts are not matched-filler precision or recall.

Important limits:

- The reference is channel A; other-speaker cross-talk can produce words absent
  from that reference. An empty channel-A reference does not prove silence or
  that every predicted word is hallucinated. Independent listening is pending.
- Boundary words must fit entirely inside the window under the existing
  reference rule. This is a diagnostic comparison, not adjudicated ASR scoring.
- The quiet case is a -18 dB variant, and overlapping windows share speech.
  Do not pool these as independent examples or population confidence bounds.
- Parakeet is run independently on each shared window. Its chunk boundaries
  differ from the prior full-excerpt diagnostic; its old numbers are not reused.
- `success` in the report means the worker completed its protocol, not that the
  text is correct. Failed/partial streams cannot contribute hypothesis metrics.

## Observed results

| Excerpt / window | Reference words | LFM3 word edit rate | Parakeet word edit rate |
|---|---:|---:|---:|
| initial-filler / 0 | 29 | 137.9% | 51.7% |
| quiet-filler / 0 | 29 | 127.6% | 100.0% |
| meaningful-like-pauses / 0 | 40 | 42.5% | 22.5% |
| meaningful-like-pauses / 1 | 41 | 19.5% | 14.6% |
| meaningful-like-pauses / 2 | 0 | — (6 words) | — (0 words) |
| later-background / 0 | 0 | — (30 words) | — (7 words) |
| later-background / 1 | 0 | — (10 words) | — (0 words) |
| later-background / 2 | 0 | — (66 words) | — (10 words) |
| later-background / 3 | 0 | — (14 words) | — (0 words) |
| heldout-introduction / 0 | 8 | 37.5% | 25.0% |
| heldout-repeats / 0 | 94 | 22.3% | 11.7% |
| heldout-repeats / 1 | 81 | 27.2% | 18.5% |
| heldout-repeats / 2 | 11 | 36.4% | 36.4% |

All 26 worker invocations completed. Parakeet returned zero words on the quiet
window; LFM3 returned 56 versus 29 reference words. LFM3 omitted the annotated
initial filler in both the normal and quiet variant. The held-out repeats
windows contain fewer predicted fillers than annotated fillers for both models
in their first two windows. Counts alone cannot establish matched precision.

The previous edge-error failures and blinded join-listening requirement remain
open. Next: evaluate an explicitly selected acoustic aligner with a
verbatim-sensitive recognizer, retain reference words through alignment, then
measure boundaries and edited joins on a larger speaker-disjoint corpus.
Forced alignment alone cannot recover words omitted by recognition.

## Reproduce

Use the existing AMI downloads and explicit local assets described in the
[word-timing instructions](../../word-timing/README.md). The output directory
must be new, so a run cannot silently mix old artifacts with fresh inference.
The supplied sandbox profile must permit owned loopback traffic while denying
external network traffic on macOS.

```sh
just test audio-recognition /absolute/obey-voice /absolute/speech.json \
  /absolute/audio-config.json /absolute/ami-wavs /absolute/annotations.zip \
  /absolute/new-evidence /absolute/loopback-only.sb
```

Python 3.11+, ffmpeg and the explicit worker/model assets are required. The
recipe first runs metric/partial-stream regression tests. Per-worker wall
time is capped; a timed-out process group is terminated and then killed if
necessary. Raw failures remain in the output. To select an individual fixed
excerpt, invoke `scripts/compare-audio-recognition.py --help` and use `--case`.
