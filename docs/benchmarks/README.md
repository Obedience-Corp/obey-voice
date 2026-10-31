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

just bench save-place S6 lan          # same fixtures, speech on the other computer
just bench place-diff a.json b.json   # full-turn local vs LAN (reported, never gated)

just bench brain                      # conversational brain-model A/B (B-series)
just bench brain-one qwen3:30b        # one model, one rep
```

## The B series: brain-model A/B

`just bench` above measures the **speech stack** from recorded audio, and
deliberately does not gate the `model` stage. `just bench brain` measures the
**model itself**, and needs no corpus: it drives `brain.ThinkStream` directly
with the real persona system prompt, the real tool schemas, and the real
`num_ctx` / `think` / `keep_alive` options.

Its gate is written down before each run and lives beside the results:

- [`B1-brain-latency-ab-criteria.md`](B1-brain-latency-ab-criteria.md) — the
  thresholds, the candidate list, and the exclusions, committed before any
  number existed.
- [`B1-brain-latency-ab-2026-08-28.md`](B1-brain-latency-ab-2026-08-28.md) —
  results and recommendation, with `B1b` (tool side effects) and `B1c`
  (tools-off floor) as the supporting runs its claims cite.
- Harness and flags: [`scripts/brain_bench/README.md`](../../scripts/brain_bench/README.md).

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
| `playback` | `PlaybackStartElapsed` — end of speech to first audible frame | No, reported. The "talk feels fast" number, and the one `place-diff` compares |

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

Speech can now run on another computer (`tts_place`), so the envelope also
records **where** it ran (`tts_place`, and a synthetic `tts_worker` label
when that is `lan`; the local hostname and token are not shared) and the configured **tier** when the engine is
qwen. Each *run* additionally carries its own `metrics.TTSPlace` and
`metrics.TTSWorkerStreaming`, because a sweep can measure several engines into
one file and the envelope has room for only one answer.

`metrics.TTSTier` is the authoritative tier. Over LAN the local
`qwen_tts_model_tier` key describes a worker this process never started, so the
provider reports the tier the worker named for itself in its ready frame
(spec/30 §2) and leaves it empty when the worker named none — rather than
labelling the numbers with a model that did not produce them.

`metrics.TTSWorkerStreaming` is the worker's `ready.streaming`. It is recorded
beside the timings because it is what makes them explicable: a false there
means the worker generates the whole utterance before any PCM exists, so first
audio *cannot* arrive before synthesis ends no matter what the pipeline does.

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
| `C1-coreml-*.json` | Engine-configuration A/B measured with the qwen3-tts-native warm-bench harness, not `obey-voice benchmark`. Records that the CoreML code predictor the engine already supports is absent from the shipped package, and that enabling it takes 0.6b from 1.34 to 0.999 RTF. Carries the engine commit, build hashes and exact invocation, and its baseline reproduces F7's 1.33. |
| `B2-qwen-ttft-mechanism-*.json` / `.md` | Why `qwen3.8-uncensored:27b` waited 4.3 s before every spoken word, and the fix. Ollama derives the prompt batch mainly from the context window (512 at or below 4096, 1024 above, 2048 above 32768, downgraded on tight VRAM) and llama.cpp keeps its last reusable checkpoint about one batch back from the end of the prompt, so at `num_ctx` 8192 a conversational turn reused nothing and re-read the persona whole. `ollama_num_batch: 128` skips 89% instead of 14%, taking the median first token from 4472 ms to 864 ms on the product path, at the cost of slower cold prompts. Criteria in `B2-qwen-ttft-mechanism-criteria.md`, written first; the `.md` carries a `Correction` section because the first version named the mechanism wrongly. Generate with `just bench ttft`. |
| `T4-qwen-cpu-vs-metal-*.json` / `.md` | Qwen3-TTS 0.6b on `QWEN3_TTS_BACKEND=cpu` against the default Metal placement, measured per segment in the pipeline rather than in the lab. CPU sustains **0.821** median RTF against Metal's **1.356**, and 33 of 35 CPU segments stay under realtime where 40 of 40 Metal segments do not; median dead air per turn falls 8.9 s → 1.8 s. RTF is flat with utterance length on both. The Metal leg reproduces F7's 1.3289 to within 2.05 %, which is what makes the CPU figure a backend difference rather than a harness artifact. Reported only — no gate and no default is changed, and audio quality was not compared. |
| `B1e-brain-qwen30b-rerun-*.json` / `.md` | `qwen3:30b` re-measured after #64/#68 filtered think blocks out of speech, with the incumbent re-measured beside it at the shipped `num_batch: 128`. Think-filtering holds (zero leaks in 60 turns), and 30b's celebrated 176 ms first token turns out to be its first *`<think>`* token: the first spoken word arrives at 4.1 s empty / 7.0 s at 6k, against the incumbent's 2.2 / 2.3 s. Adds `speak_ttft_ms` to the harness — the wait a listener hears, measured through `pipeline.SpeechProjection`, the product's own path from a model segment to TTS. **Read the `post_hoc` key in the json before quoting a number:** a live obey-voice session covered most of the run, including both headline medians, so the structural finding (30b's wait is think-generation-bound) holds while every ratio here is soft pending a quiet-machine re-run. |
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

### Qwen worker count: 1 vs 2

`qwen_tts_workers` runs more than one `bin/qwen3-tts-worker` subprocess behind
the one provider. Qwen's in-pipeline RTF is 1.83 at 0.6b, and the ramp rule
says a segment must be shorter than the previous one divided by the RTF, so no
chunking policy removes its dead air — throughput has to rise instead. Whether
a second worker raises it on one Metal GPU is unknown: autoregressive decode at
batch 1 usually leaves headroom, but two copies of the model and a 27B brain
compete for the same memory.

**The default is 1 and stays 1 until this run says otherwise.** Nothing below
has been run.

Measure in an isolated HOME so the real install is never edited, pointing
`models_dir` at the real cache so nothing re-downloads:

```bash
just build
rig="$(mktemp -d)"
root="$rig/.obey/agents/voice/obey-voice"
mkdir -p "$root"
cp "$HOME/.obey/agents/voice/obey-voice/config.yaml" "$root/config.yaml"
HOME="$rig" ./bin/obey-voice config set models_dir "$HOME/.cache/obey-voice/models"

# Leg A: one worker
HOME="$rig" ./bin/obey-voice config set qwen_tts_workers 1
HOME="$rig" BENCH_TTS_RAW=docs/benchmarks/.w1-workers1.json just bench tts "qwen3-tts:0.6b"

# Leg B: two workers, same commit, same session, same fixtures
HOME="$rig" ./bin/obey-voice config set qwen_tts_workers 2
HOME="$rig" BENCH_TTS_RAW=docs/benchmarks/.w1-workers2.json just bench tts "qwen3-tts:0.6b"
```

Then repeat both legs with the 27B brain unloaded (`ollama ps` to see what is
resident, `ollama stop <model>` to unload — reversible, and never `ollama rm`),
which is what separates the engine's own scaling from memory pressure.

Sample worker memory **during** each leg, from a second terminal:

```bash
while :; do
  pgrep -f qwen3-tts-worker | xargs -I{} ps -o pid=,rss=,comm= -p {}
  vm_stat | awk '/Pages free/ {print "free pages:", $3}'
  sleep 2
done
```

`ps` RSS counts mmapped GGUF pages that two workers may share, so it is an
upper bound on what a second worker actually costs. `vmmap --summary <pid>`
reports the dirty/private split, which is the number that settles whether two
workers fit; report both, and report what was measured rather than a
prediction.

`cmd/tts-ab` keys a comparison on the engine label, and both legs are
`qwen3-tts:0.6b`, so a 1-vs-2 A/B collides on the same engine string. Report
the two raw tables side by side (`print_tts_table` output, or the fields below
read off the raw JSON with `jq`) and say that is why; do not invent a worker
field the tool does not have.

Per leg, from the raw JSON:

| Quantity | Source |
|---|---|
| aggregate RTF | median of `Segments[].EngineElapsed / AudioSeconds`, plus total audio seconds over total wall time |
| dead air | `DeadAirElapsed` per turn |
| underruns | `Underruns` per turn |
| fallbacks / stalls | `TTSFallbacks`, `StallWatchdogFired` |
| resident memory | sampled RSS of **each** `qwen3-tts-worker`, the `vmmap` private split, and system free memory |

The ship gate (D8). All three must hold, with the 27B brain resident, before
the default may move off 1:

| Gate | Threshold |
|---|---|
| Aggregate throughput | 2 workers ≥ **1.4×** 1 worker |
| Underruns | **no increase** over the 1-worker leg |
| Resident memory | both workers inside the box's budget, brain resident |

Below any of them, `qwen_tts_workers` stays 1 and is documented as an
experiment.

### Measuring a real conversation

Fixtures will not reproduce a specific slow turn — the reply text is what
drives synthesis cost, and the prompt that caused it was yours. Any command
accepts `--transcript-log`:

```bash
obey-voice --transcript-log /tmp/session.jsonl
```

Each turn appends one `metrics` record carrying `tts_provider`, `tts_tier`,
`tts_place`, `tts_streaming`, `first_audio_grace_s`, `stall_watchdog_fired`,
and a `segments` array with `engine_s` / `queue_s` / `synth_s` / `audio_s` /
`rtf` per segment (schema v2).
To find the slow turns:

```bash
jq -r 'select(.type=="metrics") |
  "\(.tts_provider):\(.tts_tier // "")@\(.tts_place // "local")  playback_start \(.playback_start_s)s  " +
  "segments \((.segments//[])|length)  worst_rtf \([(.segments//[])[].rtf]|max // 0)"' \
  /tmp/session.jsonl
```

## Comparing speech on this computer against speech on another

The fleet design's product question is not "is the LAN worker faster at
synthesis" — it is whether moving speech off-box moves the number a listener
experiences, `PlaybackStartElapsed`. That comparison is two runs of the *same*
fixtures differing only in `tts_place`, read off their artifacts:

```bash
just build
just bench save-place S6 local     # writes S6-local-<date>.json
just bench save-place S6 lan       # needs tts_worker_url + token; run on the same host
just bench place-diff docs/benchmarks/S6-local-<date>.json \
                      docs/benchmarks/S6-lan-<date>.json
```

A turn the Kokoro fallback spoke — wholly or partly (`TTSFallbacks > 0`) — is
**excluded** from the turn tables and the place-diff medians, and the exclusion
is printed. A LAN run whose worker kept failing would otherwise be measuring
local Kokoro under a `lan` label, and an unreachable worker would read as a
fast one. When every turn on one side fell back, `place-diff` refuses with an
error instead of printing a delta.

`--tts-place` on `obey-voice benchmark` (and `BENCH_TTS_PLACE` for the script)
is what keeps those two runs one instrument. Without it the operator edits
config between them, and the difference is no longer only the place. An
unreachable or misconfigured worker fails the command outright rather than
falling back to Kokoro and writing a Kokoro measurement labelled `lan`.

**`place-diff` reports; it never gates.** `PlaybackStartElapsed` is dominated
by model think time — measured at 307 % spread, which is why the gate is the
stt stage and not this — so a full-turn delta is evidence to read alongside the
stage table, not a pass/fail. Both are printed. `just bench run` and
`just bench save` print the same full-turn table, so any artifact answers the
question without a second tool.

The LAN side needs a second machine running `obey-voice speech-serve`; there is
no way to fake it locally that would mean anything, since the whole point is
the other computer's silicon.

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

## Sharing artifacts

B1–B1f historical artifacts carry `privacy_redaction`: raw generated replies,
spoken text, nonempty diagnostics and tool arguments have been withheld, and local
paths replaced. Measurements and character counters still describe the original
run. Text-level reproduction is no longer available from these redacted records;
run a new private benchmark to investigate raw text. This limits independent
verification of qualitative conclusions in the historical reports.

`scripts/brain_bench` and `scripts/bench.sh` now filter saved benchmark JSON through
`internal/benchprivacy`. The shell benchmark requires Go as well as jq. The filter
preserves JSON numeric lexemes, removes raw output fields, and substitutes local
home/workspace paths and speech-worker addresses. Future shell benchmark artifacts
also withhold both recognized and reference transcripts from recorded corpora;
their precomputed scores remain. A standalone filter is available:

```sh
go run ./scripts/bench_redact < private-benchmark.json > shared-benchmark.json
```

The filter is deliberately narrow: it is not a secret scanner, arbitrary-prose
anonymizer, media review or rights check. Console diagnostics can remain private;
review new artifacts before sharing them. Other benchmark generators still require
manual review. The t3b transcript and reference voice clips have unresolved rights;
see [public release gates](../public-release-checklist.md).
