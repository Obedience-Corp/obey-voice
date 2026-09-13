# convoflow

Opt-in conversation harness for the live-router / worker split. **Not** part of
`just test` or CI.

Default path is local: one Ollama daemon, live brain + worker as two clients of
the same model. The knobs at the top of `.justfiles/convoflow.just` are
defaults; override one with its `CONVOFLOW_*` environment variable, or pass
harness flags after the scenario name (`just` has no named recipe arguments,
and a module's variables cannot be overridden from the top level).

```bash
just convoflow                                        # list recipes
just convoflow run                                    # search-web-intent, defaults
just convoflow run search-web-intent -live-model=qwen3.8:27b
just convoflow run job-status -live-model=qwen3:8b -worker-model=qwen3:30b
CONVOFLOW_LIVE_MODEL=qwen3.8:27b just convoflow gate  # three Layer-1 scenarios (v1 success)
just convoflow live-router                            # soft live-brain spawn
just convoflow run work-stream-gate                   # worker tool calls carry job ids; queued → started → terminal
just convoflow run work-stream-cancel                 # "kill it" aborts the running job; "Stopped after …" spoken once
just convoflow run work-stream-boundary               # a worker told to leave the campaign is refused by its tools (boundary_refusals >= 1)
just convoflow run search-web-intent -worker-provider=grok   # comparison, not the gate
```

Recordings land in `~/.cache/obey-voice/convoflow/<scenario>-<ts>/` (override
`OBEY_VOICE_CONVOFLOW_DIR`).

`just convoflow run` builds a production `voiceagent.Agent`: `brain.NewProvider`
for the live brain, `brain.NewWorkerProvider` for the worker (same daemon, same
model unless you say otherwise), the configured TTS, and a recording player. It
starts from your `config.yaml` (`config.Load()`), then overlays the knobs and
forces what the gate needs: `voice_tools_enabled: true`, `voice_router_tools:
route_only` (unless the scenario says `legacy`), and `work_dir` under the run
directory so worker tools resolve paths there. `config.resolved.yaml` records
exactly those knobs — never the whole config.

Before anything is built the harness probes the daemon: Ollama must answer
`/api/tags` and every model the run needs must already be pulled, or the run
exits 2 (skip). Missing TTS assets are a skip too. `VOICE_TOOLS_ENABLED=false`
in the environment is a skip: the gate cannot prove tools on the worker.

Exit codes: `0` every expect passed, `1` an expect or the run failed (the run
dir is still complete), `2` skipped.

Each run leaves `scenario.yaml`, `config.resolved.yaml`, `events.jsonl` (every
bus event, written as it happens), `metrics.json`, `report.md` (resolved
models, per-turn route and spoken lines, audio scores), and `work/`.

Playback is captured by a recording `audio.Engine`. Real playback is enabled by
`-speakers` or the scenario setting `capture.speakers: true`. The `work-survival`
scenario enables speakers because its communicated-result and receipt expectations
require an audible sink; recording alone leaves the result pending as
`audio_unavailable`. Each turn writes `audio/<nn>-<id>/playback.wav` and
`analysis.json`. `just convoflow analyze RUN_DIR` rescores those WAVs.

Job lifecycle names in expects: `job_queued` (the table accepted the job),
`job_started` (its run began), `job_terminal` (finished, failed, or aborted);
`wait: job_terminal` ends on any of the three. A `tool_call_started` that
carries a `job_id` is the worker's and counts in `worker_tool_calls`;
`live_tool_calls` counts only the talking brain's own tools, and `route` is
`live_router_hit`.

Two no-model paths exist, neither is the gate:

```bash
go run ./tests/convoflow -self-test -scenario tests/convoflow/scenarios/self-test.yaml
just convoflow run search-web-intent -worker-provider=stub   # real live brain + TTS, worker answers "done"
```

`-self-test` refuses any other scenario and needs nothing installed. The stub
worker is injected through `Options.Worker` and never written into config;
`just convoflow gate` refuses it. `go test ./tests/convoflow` needs no models.

Gate YAMLs assert `crackle_failing_wavs == 0` once playback WAVs are scored.
Asserting that metric without a recording player still fails closed. A WAV
fails on more than 8 impulses (clicks), a clipped peak, or an exact-zero run
of 250 ms or longer inside speech (a stall or padded frame). The package's
near-silence counters are reported as `mid_silence_runs` but do not gate: a
vocoder's output goes near zero at every pause, so that count is a property
of speech, not a defect.

## Recorded speech and paired latency artifacts

A speech scenario uses `input: speech` and a `wav` path on each spoken turn.
The harness opens a finite WAV source in real time, runs the configured local
VAD/STT, and passes that input through the same long-lived production pipeline.
The brain, worker coordinator and history survive between fixtures. It never
opens a microphone. Paths are relative to the invocation's working directory;
use absolute paths for external fixtures. WAV playback is recorded; `-speakers`
explicitly enables the real output tee. Fixture playback does not exercise the
microphone ring buffer or measure barge-in.

For an isolated measurement, add `latency_condition: baseline` or `worker` to
the scenario and `latency_iteration: 1` (then 2, 3, …) to each measured WAV turn.
Label warmup turns with `latency_warmup: true`; this is not a claim that the
model was cold. Use an explicit `-fixture-config` for every latency run. The
ordinary `user` string is not substituted for recorded speech.

```yaml
name: recorded-latency
input: speech
latency_condition: baseline
capture: {audio: true, speakers: true}
turns:
  - id: warmup
    wav: /absolute/path/to/recorded-question.wav
    latency_iteration: 1
    latency_warmup: true
  - id: measured
    wav: /absolute/path/to/recorded-question.wav
    latency_iteration: 2
```

`metrics.json` retains raw production turn offsets, wall time for correlation,
WAV content hash, actual worker/pending counts at admission, input/sink kind,
errors and a fingerprint of the effective configuration, running executable,
OS, architecture and host. The fingerprint excludes only the run's working
directory; it stores no credentials. Timings use monotonic duration fields,
not subtraction of serialized wall-clock times. Failed turns retain partial
playback WAVs, analysis and their timing/error record. Non-completed production
turns fail the measured run even when the pipeline's no-speech return has no Go
error.

Compare two generated `metrics.json` files with the same built harness and
pinned configuration:

```sh
go run ./tests/convoflow -compare /path/to/baseline/metrics.json,/path/to/worker/metrics.json
```

Comparison requires at least 30 non-warmup samples per condition and matching
WAV/iteration populations, provenance and warmup labels. Baseline samples must
start with no running jobs; worker samples must observe at least one running
canonical job. Both exclude pre-existing pending updates. Run an actual worker
scenario before the measured WAVs; the condition label does not create a worker
or invent contention. Keep shared-endpoint and independent-endpoint experiments
as separate pairs. Changing the executable or pinned configuration requires a
new pair.

The report calculates nearest-rank p50/p95 from finalized input to local
playback start. It rejects missing/out-of-order milestones, failed samples,
text input, recording-only output and incomplete runs. A successful comparison
establishes comparable timing evidence only: it does not enforce the 10% release
regression decision or certify hearing, UI handoff, safe yield, lost audio or
barge-in. Those require separate observed evidence. There is no automatic
30-turn workload generator or fabricated microphone baseline.

## Physical acoustic acceptance

Physical acceptance uses `capture.microphone: true` and a recorded speech WAV.
The harness plays the WAV through the selected speaker and recognizes what the
microphone actually receives through the production capture, VAD, and STT
pipeline. Set `expected_transcript`; comparison is strict after Unicode
case-folding and punctuation/whitespace normalization. A run is accepted only
when the transcript is non-empty and matches, microphone RMS is at least 0.005,
the peak is below 0.999, and the 10 ms RMS-envelope identity score is at least
0.60. Raw waveform correlation is retained for diagnostics but is not the
acceptance identity metric because room reflections and independent device
clocks make it unstable.

Every attempt retains `stimulus.wav`, `mic-in.wav` when any capture arrived, and
`metrics.json`. Failed attempts retain the same artifacts where possible and
include an `error` field; missing transcript, silence, clipping, weak envelope
identity, playback failure, and cancellation are failures. The artifacts are
diagnostic evidence only: they do not claim human audibility or prove a
particular room, speaker placement, or acoustic path will generalize.

For a latency pair, use the same built harness and fixture config for every
run, with three warmups followed by at least 30 measured turns per condition.
Run the baseline and worker conditions serially, never concurrently, and use
separate endpoint resource identities when comparing independent endpoints.
The baseline has no active worker; the worker condition sets
`worker.sustained_load: true` and explicitly starts a latency worker before
each measured user task and stops it immediately afterward. A scenario must
use the acoustic production watcher with `wav` and `interrupt_wav` for an
interruption turn; do not inject a manual cancel. Human attendance is useful
for setup but is not a gate.

The generic recipes are:

```bash
just test acoustic SCENARIO CONFIG RUNS
just test work-latency-compare BASELINE WORKER
```

`CONFIG` is the saved fixture config used for the whole pair. `RUNS` is the
number of complete scenario executions; the recipe requires an already-built
`CONVOFLOW_BIN` so every run uses the same executable. The comparison command
requires matching provenance, WAV populations, and warmup labels and reports
the evidence without making a release decision.

Envelope correlation checks the acoustic path, not phrase identity: different
short utterances can have similar volume patterns. The transcript must also
match every word after case, punctuation and zero-through-ten normalization.
The initial physical calibration matched the reference phrase at correlations
of 0.68–0.88; failed calibration runs remain retained with their original
threshold and measurements.
