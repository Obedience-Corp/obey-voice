# T4 — Qwen3-TTS 0.6b: CPU backend vs Metal, in the pipeline

**2026-08-30 · Apple M4 Max, 128 GB, macOS 26.5 · engine `22277bc` (same as F7) · tier 0.6b**

- Raw artifact: [`T4-qwen-cpu-vs-metal-2026-08-30.json`](T4-qwen-cpu-vs-metal-2026-08-30.json)
- Harness: `obey-voice benchmark --full-turn-fixture` — the committed per-segment
  TTS bench, `events.SegmentMetric`
- Lab counterpart: [`F7`](F7-qwen-tts-17b-metal-2026-08-26.json) (Metal, out-of-tree warm-bench)

## The question

`qwen_tts_stream: auto` streams Qwen's PCM only where streaming was measured not
to break up: `tts_place: lan`, or a local package whose `backend_hint` is `cuda`.
Local Metal is excluded because 0.6b measures ~1.30–1.36 RTF there, and a
synthesizer slower than realtime starves a streaming player. An earlier
unrecorded spot-check put `QWEN3_TTS_BACKEND=cpu` near 0.80 on this Mac, which
would clear the bar. This run checks that properly.

**It changes no gate and no default.** It supplies the number for a decision
made elsewhere.

## Answer

**Yes on both counts. CPU sustains RTF below 1.0; Metal never reaches it once.**

| | Metal (default) | CPU (`QWEN3_TTS_BACKEND=cpu`) |
|---|---:|---:|
| segments measured | 40 | 35 |
| **RTF median** | **1.356** | **0.821** |
| RTF best observed | 1.306 | 0.799 |
| RTF p95 (nearest-rank) | 2.657 | 1.009 |
| **segments ≥ 1.0 realtime** | **40 / 40** | **2 / 35** |
| first segment of a turn, median | 2.273 | 0.950 |
| later segments, median | 1.335 | 0.810 |
| median dead air per turn | **8.9 s** | **1.8 s** |

CPU synthesizes the same work **1.65× faster** than the Metal placement. Not one
Metal segment in forty kept up with playback; the *fastest* Metal segment
measured (1.306) is still 60 % slower than the CPU median.

### Flat with length — both, and CPU's line sits below 1.0

Median RTF by segment audio duration:

| audio length | Metal | CPU |
|---|---:|---:|
| 0–2 s | 1.381 *(n=4)* | 0.887 *(n=1)* |
| 2–4 s | 1.373 *(n=6)* | 0.839 *(n=6)* |
| 4–6 s | 1.361 *(n=8)* | 0.818 *(n=10)* |
| 6 s+ | 1.334 *(n=22)* | 0.839 *(n=18)* |

Neither backend degrades as the utterance grows — the difference between them is
a level shift, not a slope. CPU stays under realtime in every bucket, out to its
longest segment at 10.697 s (Metal's longest is 10.137 s). That is the shape the
fleet design asks for.

### The first segment is where both backends hurt

| | first segment | later segments |
|---|---|---|
| Metal | median 2.273, range 1.649–2.853 | median 1.335, max 1.720 |
| CPU | median 0.950, range 0.908–1.009 | median 0.810, max 1.473 |

Both pay a first-segment premium, and it lands on time-to-first-audio — the wait
a listener actually feels. On Metal it is 1.7× the steady state. On CPU it
survives but still lands at or just under realtime.

### What the listener gets

Median **dead air** — the silence the player spends idle between sentences,
waiting for the next one to exist — falls from **8.9 s to 1.8 s per turn**, an
80 % reduction. That is the direct consequence of crossing 1.0.

`PlaybackStartElapsed` fell from 22.7 s to 12.1 s, but do not attribute that to
the backend: it is dominated by brain think time on a 20 tok/s local model. It
is in the artifact for completeness.

Underruns were zero on both legs, as they must be — this worker is
`streaming: false`, so a segment is complete before it is audible. Underruns are
what a *streaming* path would produce, which is the path this measurement exists
to inform.

## Method, and why each choice

- **RTF is `SegmentMetric.RealtimeFactor()`** — `EngineElapsed / AudioSeconds`,
  synthesis alone. Not `SynthElapsed`, which includes the wait behind an earlier
  sentence's playback and would report a healthy engine as broken.
- **Iteration 1 of 4 discarded** on both legs, matching `cmd/tts-ab`'s
  `-drop-warmup` default: a cold worker pays 7–11 s of model load on its first
  synthesis. 3 iterations × 3 fixtures = 9 measured turns per leg.
- **Fixtures** are the committed `SYNTH_TTS_TEXTS` long-reply set from
  `scripts/bench.sh`, regenerated through `cmd/golden-tts` with the same 1.5 s
  `apad` that `ensure_synth_fixtures` applies, into a scratch directory via
  `OBEY_VOICE_BENCH_FIXTURES` so `~/.cache` was not modified.
- **Legs ran strictly one after the other**, never concurrently, on an otherwise
  idle machine.
- **The brain writes the reply**, so segment texts differ between legs. RTF is
  normalized by audio duration and reported per length bucket for exactly that
  reason; no cross-leg claim rests on identical text.
- **The env var does reach the worker.** `qwen_native_session.go` builds the
  worker environment from `os.Environ()` — it strips `QWEN3_TTS_TIER`/`MODEL`
  and rewrites the library paths, and passes everything else through. Nothing in
  obey-voice sets `QWEN3_TTS_BACKEND`; this run set it in the parent shell. The
  result is the proof: same binary, same fixtures, same tier, 1.36 → 0.82.
- **Cross-check.** `F7` measured this engine at 1.3289 with a different harness
  out of tree; this run measures 1.3562 in the pipeline. Two instruments,
  **2.05 %** apart — which is what makes the CPU figure a backend difference
  rather than a harness artifact.

## What this does not settle

- **Audio quality.** Nobody listened to the two legs side by side. A backend
  that is faster and worse is not a win, and there is no evidence here either
  way.
- **Whether the gate should change.** The gate reads the installed package's
  `backend_hint` (`metal` here), not an ambient environment variable, and
  obey-voice never sets `QWEN3_TTS_BACKEND`. Acting on this means deciding how a
  CPU placement gets selected and recorded — a separate decision.
- **The 1.7b tier**, where dead air was worst. Only 0.6b was measured.
- **Thermals under sustained load.** Nine turns is minutes, not an hour.
- **The cost to everything else.** The CPU backend spends cores the local brain
  also wants. On a box running an LLM and speech at once, that trade is real and
  is not measured here.
