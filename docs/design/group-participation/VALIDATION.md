# Design validation evidence

Validated 2026-10-28 using the revisions in AUDIT.md. This is validation of specs,
existing seams and isolated text inference. It is not a production group feature,
audio acceptance result or release qualification. Raw local artifacts are in
[evidence/](evidence/); no personal config/session files were changed.

## Contract and existing-runtime checks

| Check actually run | Result | What it establishes |
| --- | --- | --- |
| `uv run --with jsonschema==4.26.0 python contracts/verify.py` | Pass: 3 decision schemas, 3 host schemas; 3 valid examples, 8 invalid decisions rejected, 7 trace witnesses, 15 fence cases | Artifact consistency and reference safety predicate; witness checks do not execute production scheduling. |
| Focused Go checks below | Pass in all 3 packages: runtime 1.192 s, pipeline 0.571 s, brain 0.782 s | Existing semantic refusal, compacted continuity, immutable admission, legacy continuation and interruption seams remain suitable foundations. |
| Same focused checks after source refresh to `865a245d` | Pass: runtime 1.392 s, pipeline 0.397 s, brain 0.649 s | Merged mention/menu/worker changes preserve audited discussion foundations. |
| Initial v1 recorded model outputs through `verify.py --probe` | Expected exit 1; per-case failures retained in `evidence/contract-results-v1.json` | Schema shape alone does not establish correct audience, membership, grounding or eligibility. |
| Qwen3.8 v2 original corpus through schema + host checks | Exit 0, 15/15 | Correct outputs on the inspected development corpus for this prompt/model. |
| Qwen3.8 v2 additional unseen corpus | Exit 1, 5/6 | Remaining audience omission blocks model release qualification. |

Exact focused existing-runtime command, run from `projects/obey-voice`:

```sh
go test ./internal/persona/runtime ./pkg/voiceagent/pipeline ./pkg/voiceagent/brain \
  -run 'Test(Semantic|GroupHandoffWireAndDisk|AgentHandoff|PersonaRoundTrip|HandoffProposal|TextContinuation|Continuation|BackgroundTurnDoesNotInvokeHandoff|VoiceInterruptDoesNotStartHandoff|OptionalHandoff|NewSpeechCancelsHandoff|InterruptedSuccessfulProposal|HandoffCommitsOnlyAfterSpeechWatcher|SilentHandoff)' \
  -count=1 -timeout 120s
```

This selected package run is narrower than full project checks. No production
source changed, so full engine/client builds were not used as a substitute for
design or UI proof. Tests construct isolated fixtures; the probe uses synthetic
payloads directly against loopback Ollama, with no speech devices or app config.

## Local inference experiment

Ollama 0.40.12. Model digests and package/repository versions are retained in
`evidence/environment.json`. Requests: tools absent, thinking disabled, temperature
0, seed 42, 8192 context and maximum 256 output tokens. The production spec has
smaller per-purpose limits; repeat quality tests with those actual limits at
implementation. Runs are sequential and share local daemon/GPU resources.

| Prompt/model | Audience exact result | Median audience HTTP wall time |
| --- | --- | --- |
| v1, Qwen3:8b | 8/15 | 631.98 ms |
| v1, LFM3:1.7b | 2/15 | 337.39 ms |
| v1, Qwen3.8:latest | 14/15 by kind/member/coverage only; several group reasons invalid | 3033.45 ms |
| v2, Qwen3.8:latest | 15/15 including host constraints | 3266.55 ms |
| v2, Qwen3.8:latest, new unseen phrasing | 5/6 including host constraints | 3390.47 ms |

The v1 prompt failed to specify a valid reason enum for group decisions. v2
corrected that omission and clarified that third-party groups are not an audience
invitation. The original 15 cases were inspected during this revision, so they
are development evidence, not blind accuracy. Six new cases were authored after
freezing v2 and tested once without retuning the prompt on them.

The failed unseen request was “Could one of you offer a useful counterpoint?”
The model returned p1/p2 while the configured audience was p0/p1/p2, with no
exclusion. That is an audience omission under this spec, even though relevant-only
coverage need not make all members speak. Do not silently relax the expected
result to report a pass. All other unseen cases, including third-party mentions,
a direct summary request, exclusions and “each of you,” matched expectations.

Private next-speaker/check probes additionally exposed ineligible selections,
unhelpful returning-speaker intents and malformed output. The deliberately forced
`no-eligible-yield` inference sometimes invented a candidate; the production
host must skip inference entirely when no candidate is eligible. Model claims
about corrections/revisions are not proof of useful discourse.

## Resource comparison and architecture decision

The v1 transport experiment also issued strategy-shaped private workloads after
one synthetic A/B exchange. Counts and aggregate wall time, with **no full reply,
TTS, pipeline or physical playback**, were:

| Model | Selected (2 calls) | Shortlist (3 calls) | All members (4 calls) |
| --- | --- | --- | --- |
| Qwen3:8b | 1630.39 ms | 1428.84 ms | 1931.42 ms |
| LFM3:1.7b | 1121.87 ms | 1282.07 ms | 2754.95 ms |
| Qwen3.8:latest | 4698.02 ms | 7004.37 ms | 11425.73 ms |

Qwen3.8 aggregate reported prompt-evaluation tokens were 729 / 1202 / 1620.
Raw records retain per-request load/prefill/generation durations and token counts.
Model initial load and cache state affect timing; Qwen3.8's first audience call
took 12012 ms, versus a roughly 3 s warm median. They are not a hardware guarantee.

Known experiment limitation: v1's selected workload checked p0 even when the
coordinator chose another key. The current script follows the selected key; the
old evidence is preserved. These are call-shape workloads, not equivalent
end-to-end strategy trials. Fixed order/cache effects and one sample per workload
also prevent a statistically meaningful speed or quality ranking. The Qwen3:8b
shortlist was faster in this sample despite one more call, illustrating that limit.

Select coordinator + one candidate check for the implementation because its
fan-out is bounded and persona ownership remains explicit. During all-coverage,
deterministic ordering removes coordination calls entirely. Retain shortlist/all
as experimental alternatives; current evidence does not justify their added
inference or prove that centralized selection is semantically sufficient.

Request accounting is specified algebraically in DECISIONS.md. Zero idle polling
is a required production invariant, not something raw text probes can establish
about an unimplemented controller. First audio, inter-speaker gap and contention
under production load remain unmeasured.

## Remaining proof and release status

No tested model is release-qualified by this evidence. Qwen3.8 v2 is the best
current development candidate, with 20/21 audience cases across the two corpora,
but the unseen omission and limited dialogue evidence block enablement. Smaller
models were faster and materially less correct in v1; do not select them solely
for latency. No automatic model/provider substitution is part of the design.

The specification is ready for implementation with feature disabled. Required
production proof is assigned explicitly in TASK_BREAKDOWN.md: real ticket/race
behavior and persistent state, private full-persona projection, useful independent
perspectives and A→B→A, actual speech/capture priority, TTS latency, worker delivery,
UI controls and reconnect/resume. These are future implementation gates, not
completed design validation claims.

No unresolved product preference prevents implementing the specified defaults.
Model qualification and production audio/client evidence are technical release
gates. Spec completion must not mark those gates passed.
