# Word-timing evidence — 2026-10-29

Design: **WI-37d92f**, local speech follow-up to Keepshot Editor KA0001 / WI-9bcdae.
This is a diagnostic result, **not qualification for automatic cuts**.

The [AMI Meeting Corpus](https://groups.inf.ed.ac.uk/ami/corpus/) supplies the
human speech and manual word annotations under CC BY 4.0. Credit: AMI Consortium.
The derived reference/recognizer text in `evidence-2026-10-29` retains that
attribution. No private recordings or synthesized speech were used.

Runtime: macOS 26.5, Apple Silicon, sherpa-onnx 1.13.8 CPU, pinned int8 Parakeet
TDT 0.6B v2. The [machine-readable report](evidence-2026-10-29/report.json)
records binary, source audio, extracted PCM-WAV and annotation hashes. Raw JSONL
and reference words are retained beside it. The archive revision is emitted in
`accepted`; no confidence is manufactured from token log probabilities.

## Method and result

ES2002a channel 0 / agent A / MEE006 is the calibration speaker. ES2003a
channel 0 / agent A / MEE011 is held out from these implementation iterations;
that is not a claim that it was absent from the model's training data. Excerpts
were fixed from the annotations before decoding the second speaker. The quiet
case attenuates the same calibration clip by 18 dB; it is not independent speech.

The harness aligns normalized tokens by minimum edit distance and requires
matching starts within one second. Precision/recall concern explicit spelling
fillers (um/uh/ah variants), not contextual filler classification. Edge errors
compare matched fillers' raw start/end estimates with AMI's manual boundaries;
percentiles use nearest rank. Precision lower bounds are one-sided 95% exact
binomial bounds per fixture. This small dependent sample does not support a
population safety claim. Overlapping speakers and channel cross-talk can create
hypotheses absent from the channel-A reference; the report counts overlap and
these should be adjudicated by listeners before interpreting false positives.

A later [identical-input LFM3 / Parakeet comparison](../audio-jobs/evidence/recognition-comparison.md)
uses these same fixed excerpts and fresh paired inference on shared PCM windows.
It measures text recognition only; it does not replace the timing results below
or qualify either provider for automatic cuts.

| Fixture | Result | Filler recall count | Filler precision count | Precision lower bound | Filler edges median / p95 ms |
|---|---|---:|---:|---:|---:|
| initial-filler | completed | 1/1 | 1/2 | 2.5% | 135 / 260 |
| quiet-filler | completed | 0/1 | 0/0 | unavailable | unavailable |
| meaningful-like-pauses | completed | 4/5 | 4/5 | 34.3% | 195 / 910 |
| later-background | rejected | 0/0 | 0/0 | unavailable | unavailable |
| heldout-introduction | completed | 1/1 | 1/1 | 5.0% | 240 / 420 |
| heldout-repeats | completed | 8/12 | 8/8 | 68.8% | 90 / 1400 |

The contextual excerpt includes meaningful “like” in “I kind of like whales.”
It remains in the transcript and is not proposed by spelling-only cleanup.
The held-out repeats excerpt covers stutters/repeats and overlapping speech.
The later/background excerpt has no target-speaker reference words; cross-talk
produces a zero-duration word and the entire job is rejected. Earlier segments
are not usable after a failed terminal event.

The old utterance detector omitted the initial “um” and quiet lead-in in the
calibration clip. Word mode now covers all PCM with bounded windows. That fixes
that particular omission but does not fix the model's quiet-speech failure,
normalization, hallucinations or inaccurate acoustic ends. Adjacent native
float32 frame boundaries also differed by one microsecond; converting to the
PCM sample clock fixes numerical overlap without repairing real overlaps.

## Product gates still open

- Precision ≥98% with a one-sided 95% lower bound ≥98%: **not demonstrated**.
- Recall ≥80%: **not met** by the quiet and held-out repeat fixtures.
- Edge median ≤40 ms and p95 ≤100 ms: **not met**.
- ≥95% acceptable blinded joins and independent second-listener adjudication:
  **not run**. UI preview/export checks are not listening-quality qualification.

`batch_qualified` stays false. The editor requires individual review and must
not use transcript gaps as evidence of silence. The next quality work is to
compare a verbatim-sensitive recognizer plus acoustic forced alignment on a
larger speaker-disjoint set, then run blinded join review. Changing a prompt or
padding every word is not evidence that these gates passed.

## Reproduce

Download the two headset WAVs and manual annotations using the
[official AMI download page](https://groups.inf.ed.ac.uk/ami/download/):
`ES2002a.Headset-0.wav`, `ES2003a.Headset-0.wav`, and
`ami_public_manual_1.6.2.zip`. Keep the WAV basenames in one directory.
Install speech assets explicitly using the [word protocol setup](../word-transcription.md).
Then run from the repository root:

```sh
python3 scripts/measure-word-timing.py \
  --binary /absolute/path/to/obey-voice \
  --speech-config /absolute/path/to/speech.json \
  --audio-dir /absolute/path/to/ami-wavs \
  --annotations /absolute/path/to/ami_public_manual_1.6.2.zip \
  --output /absolute/path/to/new-evidence
```

The script requires Python 3.11+ and ffmpeg, performs no download, retains failed
runs, excludes partial streams from metrics, and records hashes on every run.
Reencoding audio can change recognition: the editor UI check used an AAC MP4 of
the first excerpt, whereas this table uses the specified PCM WAVs.
