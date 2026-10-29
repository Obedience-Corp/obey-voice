# Optional decision routing: actual pipeline replay

The local decision model avoided speaking-model calls on two kinds of simple
work request. It remains an opt-in experiment: this replay does not establish
unchanged latency on ordinary turns or broad routing reliability.

The comparison contains 24 distinct synthetic cases, each repeated four times
in both modes: 96 baseline and 96 optional turns. Within each case/repetition,
mode order alternates; case order rotates each pass. The question and cases were
fixed before collection. Every observation is retained in [paired.jsonl](paired.jsonl),
including missing audio and wrong routes. Repetitions are not independent cases.

| Result | Baseline | Optional |
| --- | ---: | ---: |
| Actual admission + speaking-model HTTP calls | 205 | 191 |
| Accepted decision shortcuts | 0 | 8 (2 distinct cases) |
| Route matches labeled intent/tier | 77/96 | 80/96 |
| Turns with PCM audio | 93/96 | 92/96 |
| First PCM median, where present | 5.403 s | 5.193 s |
| First PCM p95, where present | 8.979 s | 7.783 s |

All eight decision proposals were correct, admitted by the worker host, started
the controlled worker, and produced acknowledgement audio. They were repetitions
of `small-python` and `small-list`; their call counts fell from 22 to 8, accounting
for the entire 14-call reduction. The other 88 pairs made 183 calls in each mode.

For those shortcut pairs, first PCM arrived a median **6.002 seconds earlier**
across the seven pairs with audio in both modes. Optional first PCM was
1.432–2.104 s and worker entry 1.153–1.824 s. Only two corresponding baseline turns
actually started the correct worker; on those two pairs, worker entry improved by
3.921 s and 5.251 s. The other six baseline outcomes are route failures, not
missing worker timings that can be imputed as successful work.

Across all 89 pairs with audio in both modes, optional-minus-baseline first PCM
had mean −372 ms and median **+27 ms**. Among the 82 non-shortcut pairs with audio
in both modes, mean was **+120 ms** and median **+405 ms**, with differences ranging
from −4.311 s to +6.193 s. Mean admission difference over all 96 pairs was −8 ms.
The marginal medians in the table describe different populations; the paired
results do not justify a general speedup or no-slowdown claim. Speaking-model
output and route variation remain substantial.

Three baseline and four optional turns emitted no PCM. Three of those seven
were existing speaking-model routes with no tier, rejected by worker admission;
four emitted no usable reply. The API returned no Go errors, so zero transport
errors must not be confused with complete spoken responses. `correct` in the raw
rows measures only labeled routing, not answer quality or audible success. The
optional model abstained on all four missing-audio optional turns.

## What was measured

- Apple M4 Max, 128 GiB, macOS26.5; Ollama0.32.14 and local
  `qwen3.8:latest` (27.3B Q4_K_M), context32768, batch128, thinking disabled.
- Managed Mapika Decider2B revision `533964dae8be954c5b5e19fa4948e48408094c1e`,
  runtime `50d0be0d7cb43d2066965ce5fa7f3fe4e489a60f`, route-choice-v2,
  confidence≥0.90, margin≥0.20, 750ms inference deadline.
- Actual production voice pipeline, local addressee resolution and speaking-model
  HTTP requests, actual Agency/queue admission, real local Kokoro `af_heart`.
- Final-transcript fixtures replace microphone/STT. A controlled worker writes a
  temporary marker; its entry time is measured, not full task execution.
- A software sink drains genuine PCM. This is first audio availability, not
  physical speaker onset, listening quality or naturalness.
- The decision server stays resident in both modes; baseline disables decision
  requests. Model residency pressure and cold setup are separate questions.
- One full-pipeline warmup is retained and excluded from the 192 measured turns.
  The replay ran under Go's race detector with no concurrent builds.

The manifest in the raw file records case/question/binary hashes and persona
hash. [environment.json](environment.json) records the speaking-model digest.
[summary.json](summary.json) includes denominators, distributions and every case.
Initial managed installation separately succeeded in61s, cold server readiness
in16.54s, one request in142ms and owned shutdown in30ms on this machine. Those
single smoke timings are not distributions or part of the warm comparison.

The earlier classifier-only validation accepted four distinct cases. Actual
pipeline coverage above is the relevant delivery result; classifier coverage
must not be substituted for it. Linux hardware and hosted services were not
measured. No new hosted inference was used for this delivery.

## Reproduce

See [setup and replay instructions](../../decision-routing.md). The exact corpus
is [worker-routing.json](../../../testdata/decision/worker-routing.json). The
collection recipe refuses to overwrite an output file. Summarize a completed
population with:

```sh
python3 scripts/analyze_decision_pipeline.py results.jsonl \
  testdata/decision/worker-routing.json > summary.json
```

The intended use is opt-in testing of a narrow routing shortcut. Keep baseline
fallback and host worker policy intact; do not enable it by default on the strength
of these two accepted cases.
