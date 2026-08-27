# Latency benchmarks

Committed measurements of how long a voice turn actually takes, plus the recipe
that diffs a new run against them.

The rule this directory exists to enforce: **no latency change merges on
assertion.** A claim that something got faster is checked by a committed
before/after artifact, not by a reviewer's impression.

## Commands

```bash
just bench baseline   # measure and (re)write baseline.json — defines the reference
just bench diff       # measure and compare; fails on >10% regression
just bench run        # measure and print, writing nothing
just bench save L1    # measure and write L1-<date>.json (per-item delta artifact)
just bench tts        # measure TTS engines over long-reply fixtures
```

## What is measured, and what is gated

Runs drive whole voice turns from recorded audio (`--full-turn-fixture`), so the
measurement covers capture → VAD → STT → brain → TTS → playback.

Timings are reported **per fixture, per stage** — never pooled. Pooling
different utterances into one median mixes populations: a 1.5 s clip and a 4 s
clip have genuinely different stage timings, so a pooled figure moves when the
fixture mix changes rather than when performance does.

| Stage | What it is | Gated? |
|---|---|---|
| `stt` | Endpointing + recognition | **Yes.** Measured at ±0.6 % run to run |
| `model` | First model chunk after the transcript | No — swings 1.5 s–24 s on a large local model, and nothing in this track changes it. Gating it would produce only false failures |
| `synth` | First segment → first audio (first-sentence synthesis) | No, reported |

The gate is the **median stt stage across fixtures**, with a 10 % tolerance
against a measured 0.6 % noise floor — a ~16× margin.

That choice is deliberate: the VAD-window and STT-provider changes move the stt
stage directly, while end-to-end `PlaybackStartElapsed` is dominated by model
think time. An end-to-end gate measured 307 % spread and could not have detected
a 300 ms improvement; the stage gate detects a 500 ms one at +17 %.

**Fixtures.** A recorded corpus in `testdata/corpus/` is used when present. When
it is absent, the harness generates deterministic synthetic speech from a fixed
sentence list using the project's own TTS, so a baseline never blocks on a
recording session. Synthetic fixtures are reproducible and good for regression
detection, but they contain no natural mid-sentence pauses — they cannot judge
endpointing behaviour. The fixture kind is recorded in provenance and a diff
warns when it changes.

## Provenance is not optional

Every artifact records machine, CPU, OS, samantha commit, worktree cleanliness,
persona, brain provider, **brain model**, and TTS provider.

Hardware is the obvious one. The **model matters just as much**: the same code on
the same Mac produces entirely different numbers under a 135 M model than a 26 B
one. A baseline that does not say which model produced it cannot be compared
against, so `just bench diff` warns loudly when either the machine or the model
differs from the baseline's.

## Files

| File | What |
|---|---|
| `baseline.json` | The canonical reference. Overwritten only by a deliberate `just bench baseline`. |
| `<item>-<date>.json` | Per-item delta artifacts, e.g. `L1-2026-08-12.json`. The item id joins back to the requirement it closes. |
| `<item>-tts-ab-*.json` | TTS A/B decision records (`obey-voice.bench.tts-ab.v1`). Generate with `just bench tts-ab` — see below. |
| `N7-tts-bake-off-*.md` | The 2026-08 candidate review. Its "item 3's per-candidate measurement outstanding" is what `just bench tts` now makes mechanical. |
| `<item>-stt-ab-*.json` | STT A/B decision records (criteria fixed in advance, then the numbers). `L2` kept Whisper over streaming Zipformer; `L4-stt-ab-parakeet-2026-08-19.json` flipped the default to Parakeet TDT. Generate with `just bench stt-ab` — see below. |

## STT A/B records

`obey-voice benchmark` writes **per-run** records: `transcript_score`, and a
`metrics` object whose keys are Go field names carrying int64 nanoseconds. The
stt-ab files are **aggregates** of those runs — different field names, one
level up. L2 and L4 were aggregated by hand, so a gate written against their
fields could not be evaluated from a harness run without redoing that by hand.

`cmd/stt-ab` closes that gap:

```bash
obey-voice benchmark --stt-provider sherpa-parakeet --stt-provider transcribecpp \
  --iterations 3 --audio-fixture <wavs> --expect-text <texts> --json-file raw.json

STT_AB_BACKEND=metal FIXTURE_KIND="synthetic (14 utterances)" \
  just bench stt-ab T1 raw.json sherpa-parakeet transcribecpp
```

It computes only what arithmetic settles and leaves every judgement blank:
`criteria_fixed_in_advance`, the `*_criterion_met` verdicts, `decision`,
`reading_the_numbers`, `evidence_limits`, and `durable_value` are yours to
write. It prints to stderr what is still unfilled.

Three things it records that hand-authored records did not:

- **`runs_without_a_final_transcript` counts missing finals, not empty text.**
  `STTFinalElapsed` is set only on a `FinalTranscript`, so zero means no final
  arrived even when a trailing partial left plausible-looking text behind. That
  is exactly what L2 caught — 6 of 9 runs, "fragments on the rest." Latency
  percentiles exclude those runs, so a provider that mostly failed cannot look
  fast; the failures are reported as their own count.
- **`provenance.backend`** — a Metal run that silently fell back to CPU is not
  a Metal result, and no existing key said which one ran.
- **`warmup_dropped` and `percentile_method`** — `scripts/bench.sh` drops
  iteration 1 as warm-up for its latency medians; the committed stt-ab records
  did **not** (L4 is 42 runs = 14 fixtures × 3 iterations). Both choices are
  defensible, so the artifact states which it made rather than leaving two
  files silently incomparable.

## TTS benchmarks

The commands above answer "did the STT stage regress". They cannot answer
"why does this engine take thirty seconds to start talking", for two structural
reasons, and `just bench tts` exists because of both.

**The default fixtures elicit short replies on purpose.** `SYNTH_TEXTS` asks
for one-word answers, because the gated stt stage does not care how long the
reply is and length only adds noise. Synthesis cost, though, scales with how
much audio there is — so on that fixture set TTS is nearly invisible. Every
committed artifact here was recorded on it. `SYNTH_TTS_TEXTS` is the opposite
instrument: prompts that ask for several sentences, so a turn exercises the
progressive path the product actually runs.

**The turn milestones describe only the first segment.** `FirstSegmentElapsed`
and `FirstAudioReadyElapsed` are stamped once. A reply whose later sentences
are the slow part reads as fast. `TurnMetrics.Segments` now carries every
segment's cost and audio duration, so the realtime factor is derivable per
segment rather than only from a dedicated integration test.

Each segment records **two** durations, and the distinction matters:

- `EngineElapsed` — the segment handed to the provider through the complete
  utterance being buffered. This is synthesis, and the numerator of the
  realtime factor.
- `SynthElapsed` — through the first *audible* frame, so it also includes
  waiting behind whatever was already playing.

Only the first segment's `SynthElapsed` is a delay a listener experiences.
Segment 1 is synthesized while segment 0 plays; its wait is the previous
sentence's audio, not the engine.

```bash
just build
just bench tts                                  # kokoro vs qwen3-tts:0.6b
just bench tts "kokoro qwen3-tts:0.6b qwen3-tts:1.7b"
just bench tts-ab V1 docs/benchmarks/.tts-raw.json kokoro qwen3-tts:1.7b
```

`obey-voice benchmark --tts-provider` takes `provider` or `provider:tier`
(`kokoro`, `qwen3-tts:0.6b`, `qwen3-tts:1.7b`) and is repeatable, exactly like
`--stt-provider`. The tier is part of the engine's identity, not a detail of
it: the same code measured 1.33x realtime at 0.6b and 1.87x at 1.7b (`F7`).

### What the record reports, and why each field is there

`cmd/tts-ab` aggregates a raw run into `obey-voice.bench.tts-ab.v1`. Like
`cmd/stt-ab` it computes only what arithmetic settles and prints to stderr
what is still yours to write.

| Field | Why it exists |
|---|---|
| `median_synth_stage_ms` | First segment → first audio. TTS only; excludes model think time, which no TTS change moves. |
| `median_realtime_factor` | **Engine** wall ÷ audio duration, per segment. Above 1.0 the engine produces audio more slowly than the speaker plays it. |
| `median_segment_engine_ms` / `median_segment_queue_ms` | Synthesis alone, and the wait behind earlier playback. Segment 1 is synthesized while segment 0 plays, so charging it that wait reports a healthy engine as broken — the first version of this harness did exactly that and read 3.1x where the engine was 1.9x. |
| `median_first_segment_rtf` | Time-to-first-audio waits on that segment alone. |
| `clears_batching_threshold` | Whether the candidate beats the ~2.3x realtime that makes cross-sentence batching free (D009). |
| `median_model_complete_to_audio_ms` | **Signed.** Negative means audio started before the reply finished — the progressive path working. A positive median means the pipeline waited for the whole reply. |
| `turns_without_audio` | A turn that produced silence never enters the latency percentiles; it is counted separately, so an engine that mostly failed cannot look fast. |
| `turns_with_fallback` / `segments_fallback` | Segments the configured provider failed and Kokoro spoke. `TurnMetrics.TTSProvider` names the *configured* engine, stamped before synthesis, so such a turn still carries the primary's name — it is **excluded from every latency figure** and reported only as this count. Kokoro is ~4x faster than qwen3-tts:1.7b here, so a few contaminated turns could reverse a decision. |
| `turns_stall_watchdog_fired` / `first_audio_grace_ms` | A slow turn and a hung one are the same observation until the grace expires. Qwen's `FirstAudioGrace` is 8s+12s at 0.6b and **35s+12s at 1.7b** (`pkg/voiceagent/tts/qwen.go`), so a 30s wait never trips it. The budget belongs beside the measurement. |

Engines are grouped by what the pipeline **reported**, not by what
`--tts-provider` asked for. A leg requested as `qwen3-tts` that silently ran
Kokoro is a Kokoro measurement; `requested_as` preserves the substitution, and
`tts-ab` warns on it rather than averaging it in.

### Measuring a real conversation

Fixtures will not reproduce a specific slow turn — the reply text is what
drives synthesis cost, and the prompt that caused it was yours. Any command
accepts `--transcript-log`:

```bash
obey-voice --transcript-log /tmp/session.jsonl
```

Each turn appends one `metrics` record carrying `tts_provider`, `tts_tier`,
`first_audio_grace_s`, `stall_watchdog_fired`, and a `segments` array with
`engine_s` / `queue_s` / `synth_s` / `audio_s` / `rtf` per segment (schema v2).
To find the slow turns:

```bash
jq -r 'select(.type=="metrics") |
  "\(.tts_provider):\(.tts_tier // "")  playback_start \(.playback_start_s)s  " +
  "segments \((.segments//[])|length)  worst_rtf \([(.segments//[])[].rtf]|max // 0)"' \
  /tmp/session.jsonl
```

## Verifying the gate still bites

A regression gate that has never caught anything is decorative. To confirm it
works, insert a deliberate delay where the gated stage is measured — a
`time.Sleep` immediately before `metrics.sttFinal = time.Now()` in
`transcribeTurn` — and run `just bench diff`. It must fail. Restore the code
afterwards.

Verified 2026-08-11 with a 500 ms injection. Every fixture moved by almost
exactly the injected amount (+487 ms, +554 ms, +495 ms), the gated median went
2837 ms → 3332 ms (**+17.4 %**), and the recipe exited non-zero:

```
  gated metric (median stt stage): 2837 ms -> 3332 ms   +17.4%  (fail above +10%)
  ✗ REGRESSION: stt stage is more than 10% slower than baseline
```

This is worth re-running after any change to the benchmark itself, because the
failure mode is silent: a gate that stopped comparing anything still passes.
