# T3b attribution-gap evidence

Raw inputs and outputs behind `docs/benchmarks/T3b-attribute-gap-2026-08-27.json`,
plus the commands that regenerate them. The probes are env-gated tests in
`cmd/obey-voice/cmd/t3b_*_test.go` — they skip unless the `T3B_*` variables are
set, and every one calls the shipped code (`meeting.AttributeTranscript`,
`newReplaySource`/`replayTranscribe`); nothing is reimplemented.

## Files

| File | What it is |
|---|---|
| `transcripts.json` | Step-1 output: 25 transcript records from the replay path (parakeet, offline) over the meeting fixture |
| `sherpa.analyze.json` | Control timeline: `obey-voice meeting analyze <fixture> --speakers 2 --json` (6 spans) |
| `sortformer-metal-vhl.jsonl` | Sortformer timeline: transcribe-cli v0.2.2, `very_high_latency`/metal (25 spans) |
| `attributed.json` | Step-2 output: `AttributeTranscript` over both timelines |
| `shift.json` | Step-3 output: 41-point ±2 s timestamp-shift sweep, incl. per-row `disagreements` ({id, sherpa, sortformer}) |

## Regenerate

Steps 2–3 need only this directory (deterministic; outputs reproduce
bit-identical — verified 2026-08-27, before and after the probe gained the
`disagreements` field):

```sh
D=$PWD/docs/benchmarks/t3b
T3B_TRANSCRIPTS=$D/transcripts.json T3B_SHERPA=$D/sherpa.analyze.json \
T3B_SORTFORMER=$D/sortformer-metal-vhl.jsonl \
T3B_ATTR_OUT=$D/attributed.json T3B_SHIFT_OUT=$D/shift.json \
go test ./cmd/obey-voice/cmd/ -run 'TestT3bAttribute|TestT3bShiftSweep' -count=1 -v
```

Step 1 (regenerating `transcripts.json` itself) additionally needs the local
fixture WAV and installed STT/VAD models:

```sh
T3B_FIXTURE=~/.cache/festival-voice/fixtures/meetings/product-marketing-meeting-90s.wav \
T3B_OUT=$PWD/docs/benchmarks/t3b/transcripts.json \
go test ./cmd/obey-voice/cmd/ -run TestT3bTranscribeFixture -count=1 -v
```

The fixture's identity is pinned in the artifact (`method.fixture`, 90014 ms,
sha256 `ed57895c...`); the timeline commands that produced the two `*-timeline`
inputs are recorded verbatim in the artifact's `method` section.
