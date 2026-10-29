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
barge-in. Those require separate observed evidence. The suite generator below provides the recorded fixture population and real
worker admission operations; measurements always come from executed turns.

## Physical acoustic acceptance

Prepare and repeat the suite with repository recipes:

```sh
just test acoustic-prepare /path/to/installed/models /path/to/suite full
just test acoustic-pair /path/to/isolated-config.yaml /path/to/suite
just test acoustic-controls /path/to/isolated-barge-config.yaml /path/to/suite
just test acoustic-smoke /path/to/isolated-provider-config.yaml /path/to/suite
```

Preparation builds the harness once, generates four Kokoro WAV/JSON fixtures
from explicitly selected installed models, and writes scenarios plus a hash
manifest. The optional profile defaults to `full`, preserving the existing
full-question fixture. `latency-brief` changes only the math fixture to the
short phrase `What is two plus two?`; the other three fixtures remain the same.
The selected profile is recorded in `bin/fixture-profile.txt` beside the
prepared harness binary. Unknown profiles are rejected before either build or
synthesis. It does not download models or read the user's live configuration.
The pair recipe runs three warmups and 30 measured turns per condition in
sequence, then compares the retained reports. The controls recipe exercises
quiet completion and interruption. The smoke recipe checks one physical turn
with the configured TTS provider; native Qwen can be checked using
`tts_provider: qwen3-tts` and the installed `qwen_tts_model_tier`. A smoke pass
does not establish comparable provider latency. Set the intended input/output devices,
Ollama endpoints, Parakeet model path, and frontend in the explicit fixture
config. Use a separate suite or retain the old binary before rebuilding;
comparisons reject mismatched executable or configuration fingerprints.
Start from `scenarios/acoustic-config.example.yaml`, which explicitly pins
the production 300 ms speech pre-roll and 1.2-second end-of-utterance silence.
The isolated fixture loader does not populate every application default;
omitting these values can clip words and invalidate a population.

Use a fresh suite when measuring orchestration latency with the short math
fixture, so its 33-turn baseline and 33-turn worker populations are never
pooled with a different fixture profile:

```sh
just test acoustic-prepare /path/to/installed/models /tmp/acoustic-latency-brief latency-brief
just test acoustic-pair /path/to/isolated-config.yaml /tmp/acoustic-latency-brief
```

Use a separate `full` suite for the full-question controls and regression
phrase. Do not replace failed samples or combine populations from the two
profiles; each comparison must retain one profile and its matching fixture
metadata throughout.

An existing recorded fixture cache can instead be used without synthesis:

```sh
bin/convoflow -prepare-acoustic-suite /path/to/scenarios -fixture-dir /path/to/fixtures
```

The fixture directory contains `synth-01.wav` through `synth-03.wav`,
`counting.wav`, and matching JSON files with the spoken `text`. Generated
scenarios are portable by regeneration; raw audio and machine-specific
configurations stay in the selected artifact directory.

The generated `response.yaml` uses the counting fixture for an echo-only
ordinary response control. Run it independently with:

```sh
just test acoustic-response /path/to/isolated-config.yaml /path/to/suite "Mac Studio Speakers"
```

The optional last argument selects only the initial fixture's speaker. The
control attaches real capture, STT and barge-in to the same microphone, requires
the exact initial transcript and a completed, nondegraded local-speaker
response, and plays no interruption stimulus. It immediately opens the
production next listen for the scenario's `duration` (generated as 3 seconds).
Muted effective response gain, including a small configured gain that rounds
to zero, is rejected before opening devices. Nonzero post-volume response
reference samples are required as well as successful playback events.
Any new admitted speech or user transcript fails. Early STT termination,
provider/device errors, outer timeouts, listening-time capture overflow and insufficient
captured frames also fail; they cannot count as silence. Only completion of
the declared observation window authorizes the harness to cancel and join
that otherwise-active listening turn. The configured STT start timeout must
exceed this observation window.

`acoustic/response/response.json` records response and next-listen boundaries,
actual STT sample reads, negotiated devices, effective response gain, nonzero
render count and the result. Frame coverage
allows one buffered 100ms read at the window boundary. Raw/processed capture
and the ordered render-reference trace remain with the report. This proves
only the recorded response and observation window, not indefinite echo
rejection. `acoustic-controls` now runs response, completion and interruption
in order; regenerate older suites to obtain the new scenario.

A long response can overwrite the ordinary capture ring while STT is closed.
The first next-listen frame records this historical overflow separately only
when the pipeline intentionally discarded response history, device generation
is unchanged, and timestamped retained PCM predates the explicit next-listen
boundary. Missing evidence or any later overflow fails. Render sample counts
are retained even when the control fails at the next listen.

Acoustic reports retain whole-recording levels, but the audibility gate scores
only the complete raw microphone window aligned to the fixture. It uses the
same short-window audibility criterion as production STT; the former 0.005
whole-recording RMS cutoff could reject a valid word after a long model wait
or count later assistant energy. Exact transcript, envelope correlation and
clipping checks still apply. Reports record the selected sample bounds and
window RMS so the score is reproducible.

For physical acoustic scenarios, pass `-stimulus-device "Device Name"` to
`convoflow` to route only the known fixture playback to a separate output;
capture and the assistant response continue using the explicitly configured
devices. An invalid named device fails when playback opens rather than falling
back silently. The default remains the configured output device, and each
acoustic report records the requested stimulus device separately.

Saved interruption audio can be diagnosed without reopening devices:

```sh
just test acoustic-vad /path/to/run/acoustic/interruption /path/to/isolated-config.yaml ARM_SECONDS STIMULUS_SECONDS
just test acoustic-reprocess /path/to/run-or-acoustic-artifact /path/to/isolated-config.yaml
```

Both offsets are relative to the microphone file, not the scenario start.
The command replays raw and processed capture through the production Silero
barge detector at three supported thresholds, skipping pre-arm input. When
`frontend.jsonl` is beside the WAVs, it preserves the recorded capture callback
sizes and rejects malformed or incomplete traces. Older WAV-only artifacts use
an explicitly logged 160-sample cadence. This diagnostic does not reproduce the
full gain-aware interruption policy. Its success means replay completed; the logged speech windows are diagnostic
evidence, and the physical interruption assertion remains the acceptance gate.
The reprocess command accepts either the legacy run root or a direct acoustic
artifact directory containing `frontend.jsonl`, `interruption.json`, and
`mic-in.wav`. It uses the saved ordered capture/reference trace to produce a
candidate frontend WAV and compares its production detector behavior with raw
and original processed capture. Response/stimulus timestamps from
`interruption.json` map to the start sample of the first recorded capture
callback observed at or after each event. Legacy untimed traces instead use
the first saved listening event and explicitly report that approximation. These are host
observations, not microphone ADC or speaker presentation timestamps; a
stimulus before the 600ms arm point remains a diagnosable pre-arm window.
New control traces also persist `observed_at` and monotonic `elapsed_ns` for
each serialized frontend invocation. These locate frontend processing relative
to run events; they are not microphone ADC or speaker presentation timestamps.
Historical traces without these fields retain their original timing uncertainty.

To diagnose early endpointing in a processed recording, run the production
STT loop with explicit gain metadata and read size:

```sh
just test acoustic-stt /path/to/models /path/to/mic-processed.wav /new/output-directory 6 1600
```

The gain describes amplification already applied to the WAV; samples are not
amplified again. Live capture reads 1600-sample STT frames even when device
callbacks have a different size. Outputs retain exact decoder input, transcript,
and segment operations. The gate/policy trace is a parallel evaluation of the
production functions, with assumed constant gain and accelerated pacing clearly
recorded; it does not reconstruct unsaved live gain changes, backlog or wall-clock
caps. An existing output directory is rejected to preserve prior evidence.

For an incorrect or truncated transcript, compare the clean fixture with the
complete raw and processed microphone recordings using the installed local
Parakeet model:

```sh
just test acoustic-transcribe /path/to/installed/models /path/to/artifact/stimulus.wav
just test acoustic-transcribe /path/to/installed/models /path/to/artifact/mic-in.wav
just test acoustic-transcribe /path/to/installed/models /path/to/artifact/mic-processed.wav
```

This decodes the whole WAV without VAD or endpoint segmentation and logs JSON
with the file fingerprint, model, sample information and transcript. It opens
no audio device and does not read the installed user configuration. The optional
third argument selects the Parakeet model (default `tdt-0.6b-v2`). Successful
execution means decoding completed; it does not assert fixture identity or
physical acceptance. An intact whole-file decode alongside a truncated live
transcript supports investigating segmentation. Incorrect whole-file decoding
requires checking the acoustic signal and recognition before attributing loss
to segmentation. ASR alone cannot identify the source of competing speech.

Host scheduling has a separate repeatable check:

```sh
just test work-host-timing
```

It records 30 samples each for actual Agency admission to the next production
STT start and canonical result publication to the host's background-turn
invocation. Controlled STT isolates host overhead; the quiet test uses real
capture permits, the pipeline yield protocol, and Bubble Tea dispatch. These
checks do not substitute for hardware capture or audible delivery evidence.

The native detector's reset behavior has an offline regression using installed
Silero assets and a prepared speech fixture:

```sh
just test vad-reset /path/to/installed/models /path/to/suite/fixtures/synth-02.wav
```

It feeds the same stream twice and requires identical segment offsets and PCM
after `VAD.Clear`. This catches native queue clearing being mistaken for a full
stream reset, which otherwise misaligns the next turn's speech pre-roll.


Physical acceptance uses `capture.microphone: true` and a recorded speech WAV.
The harness plays the WAV through the selected speaker and recognizes what the
microphone actually receives through the production capture, VAD, and STT
pipeline. Set `expected_transcript`; comparison is strict after Unicode
case-folding and punctuation/whitespace normalization. A run is accepted only
when the transcript is non-empty and matches, microphone RMS is at least 0.005,
the peak is below 0.999, and the 10 ms RMS-envelope correlation is at least
0.60. Raw waveform correlation is retained for diagnostics but is not the
signal-path metric because room reflections and independent device
clocks make it unstable.

Every attempt retains `stimulus.wav`, `mic-in.wav` when any capture arrived, and
`metrics.json`. Failed attempts retain the same artifacts where possible and
include an `error` field; missing transcript, silence, clipping, weak envelope
correlation, playback failure, and cancellation are failures. The artifacts are
evidence for the recorded device, frontend, and room configuration.

For a latency pair, use the same built harness and fixture config for every
run, with three warmups followed by at least 30 measured turns per condition.
Run the baseline and worker conditions serially, never concurrently, and use
separate endpoint resource identities when comparing independent endpoints.
The baseline has no active worker; the worker condition sets
`worker.sustained_load: true` and explicitly starts a latency worker before
each measured user task and stops it immediately afterward. A scenario must
use the acoustic production watcher with `wav` and `interrupt_wav` for an
interruption turn; do not inject a manual cancel. No human attendance is required.

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

`op: acoustic_completion` admits a real local worker, asks the production
capture owner to yield quiet listening, delivers the pending result, checks
one completed audible voice receipt, and resumes real listening for `wav`.
The yielded turn must contain no transcript and resumed speech must match
`expected_transcript`. `worker.sustained_load` must be disabled for this test.

`op: acoustic_interrupt` uses `wav` for the initial question and
`interrupt_wav` for speech played during the real response. It waits for
production listening before the first fixture, then waits `stimulus_delay`
after observing response playback start (default `900ms`). Set this per-turn
field to `0s` for startup speech. Optional `stimulus_complete_within: 600ms`
requires observed stimulus completion within that interval after response start;
late, canceled or unstarted playback fails even if the transcript matches.
The bound must be positive. Negative delays and use on other operations
are rejected. The omitted default keeps the quiet interval beyond the existing
600ms barge-in arm delay. Only the
production detector stops the response. The next turn must recover the full
interrupting phrase. Reports distinguish stimulus-to-stop from the monotonic
production detector-to-stop duration.

`interruption.json` retains `requested_stimulus_delay_ns`, the fixture's PCM
duration (`stimulus_fixture_duration_ns`), `stimulus_started`, `stimulus_ended`,
`response_to_stimulus_start_ns`, `response_to_stimulus_end_ns`, and
`stimulus_playback_duration_ns`. These are host observations of playback start
and completion, not speaker or ADC timestamps. A startup-short case needs
`stimulus_complete_within: 600ms`, which enforces `stimulus_completed: true`
and an observed interval wholly within 600ms of
`response_started`; setting `stimulus_delay: 0s` alone does not establish this.
Errors/cancellation retain available timing with `stimulus_completed: false`
and `stimulus_error`. Timing coverage alone does not prove interruption or
full transcript retention, which remain required independently.

Completion/interruption artifacts retain both `mic-in.wav` before frontend
processing and `mic-processed.wav` after AEC/NS/AGC. A low processed RMS alone
cannot establish physical input attenuation. Failures retain their reports
and available audio; they do not become passing latency samples.

Prepare and run repeatable immediate-onset controls with:

```sh
just test acoustic-prepare MODELS_DIR SUITE_DIR
just test acoustic-startup CONFIG SUITE_DIR
```

`startup-full` overlaps the full math question with response onset.
`startup-short` uses “Wait.” (Kokoro af_heart, speed 1.25) with a 600ms observed
completion deadline. Both retain the ordinary production interruption and exact
transcript checks. Older fixture directories without `startup-short.wav` remain
loadable; the startup recipe requires both scenarios before opening devices.

### Near-only fixture calibration

Prepared suites with `startup-short.wav` also include `calibration.yaml`: two
ordinary microphone turns, first the suite's `synth-02.wav` question, then “Wait.”.
Use the `full` fixture profile to calibrate the complete question; generation
preserves the supplied fixture and metadata, including a brief profile if used.
Neither turn overlaps assistant playback or enters a latency population.

```sh
just test acoustic-calibrate CONFIG SUITE_DIR "Studio Display Speakers"
```

The optional device selects fixture playback only. The recipe uses the suite's
existing pinned binary and explicit config without rebuilding, synthesizing,
changing gain or retrying. Each invocation retains a fresh artifact directory,
including failed runs. Both exact transcripts and the existing raw
capture checks—aligned audibility/correlation and capture clipping—must pass; “Wade” does not
satisfy “Wait.”. The scenario stops on the first failure. Passing establishes
isolated fixture capture and recognition, not audibility during overlap or
correct interruption ownership. Missing short fixtures omit the combined
scenario, and the recipe fails before opening devices; regenerate scenarios
from complete recorded fixtures or prepare a new suite first.

To retain the exact audio submitted to an offline decoder, use the optional
fourth argument:

```sh
just test acoustic-calibrate CONFIG SUITE_DIR "Studio Display Speakers" true
```

This selects `calibration-decodes.yaml`, with `capture.stt_decodes: true`.
Each turn writes `acoustic/<turn>/stt-decodes/decode-NNN.f32le`, a convenience
WAV, and JSON with sample count, hashes, transcript/error and decoder timestamps.
The float32 file preserves the exact input bits; the WAV is quantized and cannot
prove bit-for-bit equality. The observation runs synchronously after inference
and before the input can be reused. Recording failure or zero observed decodes
fails the run, including a provider using an unsupported streaming path.

This opt-in is restricted to ordinary microphone WAV turns outside timing
populations. It adds local disk work, so its timing is diagnostic. Normal runs
do not install the observer or record decoder input. These files capture what
the recognizer received; they do not establish an exact source position or
correct transcription by themselves.

Replay a recorded input without WAV conversion using the same model assets:

```sh
just test acoustic-transcribe-exact MODELS_DIR /path/to/stt-decodes/decode-001.f32le
```

The exact mode requires nonempty finite float32 little-endian mono samples at
16kHz. It rejects malformed input and reports the input fingerprint and transcript.
Compare its fingerprint with the recording JSON before comparing transcripts.
This bypasses VAD and endpointing; successful execution is a diagnostic result.

### Physical mute and provider recovery

Newly prepared suites include `mute.yaml` and `failure.yaml`:

```sh
just test acoustic-boundaries /absolute/config.yaml /absolute/suite
```

Both play the known question into the selected microphone twice. `mute` requires
one textual response with no speaker playback/reference while muted, then one
fresh, nondegraded response with completed physical playback after unmuting.
`failure` injects a labeled transport error at `ThinkStream` after real STT; it
requires one public brain error and the production degraded recovery reply, then
restores the configured provider for a healthy fresh turn. This is controlled
failure injection, not evidence of an actual remote outage. Neither scenario
contains pending background work; work-delivery mute remains a separate control.

Each retains `boundary.json` plus per-phase metrics, microphone/raw processed
WAVs, exact stimulus, ordered frontend trace, public events and work snapshots.
Silence, wrong or duplicate input, missing/duplicate response, unexpected
interruption, false audible delivery, degraded recovery of the fresh turn and
unhealthy capture fail. A successful build alone does not pass these controls.

### Repeated physical control timing

Use the same prebuilt suite binary for every attempt:

```sh
just test acoustic-control-population /absolute/config.yaml /absolute/suite completion /absolute/new-completion-population
just test acoustic-control-population /absolute/config.yaml /absolute/suite interruption /absolute/new-interruption-population
```

Each population runs three excluded warmups and 30 measured controls sequentially
in fresh child processes. The output directory must be new and its parent must
exist. No attempt is retried or replaced. A failure stops collection and keeps
its artifacts; an incomplete or failed population has no passing quantiles.
The manifest hashes the executable, scenario, config and input fixtures. Every
attempt checks those hashes and retains its exact command, child log, timestamps,
report hashes, resolved provider profile and negotiated device identities/rates.
Configuration or device drift fails the population. Model cache coldness is not
inferred from a fresh process or the warmup label.

`summary.json` reports nearest-rank p50/p95 in nanoseconds, excluding warmups.
Completion `pause` measures the call to `PauseForCompletion` through acknowledgement;
`completion` measures the entire `RunBackgroundTurn`, including inference,
synthesis and playback. These are not the UI dispatch or result-ready host-only
targets. The completion control waits for its worker result before starting the
isolated listening turn, so a cold worker cannot exhaust that turn's input timeout.
Its report records `worker_readiness_ms` separately; the 300ms pause bound starts
after this wait and is unchanged. The separate completion-speech control tests
input during completion generation.
Interruption `detection_to_stopped` is the production detector-to-stop
duration. `stimulus_to_interrupt` and `stimulus_to_stopped` are the same post-stop
event observation relative to the independent stimulus player start; they are
not two independently measured timestamps or proof of acoustic onset. The 50ms
contract allowance is a regression comparison against a comparable baseline,
not an absolute stimulus-to-stop threshold. These populations do not replace the
separate 30-turn baseline/worker reply-latency comparison.

The low-level mode is `-control-population NEW_OUTPUT` with `-scenario`,
`-fixture-config`, optional `-stimulus-device`, `-population-warmups` (default3),
`-population-samples` (minimum30), and `-population-timeout` (default3m per child).
Put provider settings in the pinned config; this mode rejects provider CLI
overrides instead of silently discarding them. Interrupting the parent cancels
and joins the owned child, retaining the incomplete attempt.

### Pending results, mute and speech during completion

```sh
just test acoustic-completion-boundaries /absolute/config.yaml /absolute/suite
```

`completion-mute.yaml` publishes one explicitly recorded weather result through
the coordinator, holds it while output is muted and microphone reads remain
healthy, then unmutes and requires one completed audible scoped receipt.
`completion-speech.yaml` holds completion generation until real microphone STT
revokes that attempt's capture lease. It requires exact retained input and one
eventual delivery. The resumed ordinary response may legitimately communicate
the result; if still pending, a fresh background turn must deliver it. No manual
lease revocation or synthetic speech-admission event is used.

Both cases play another unrelated known utterance and ensure the original receipt
stays unchanged with no result projection or replay. The first result-delivery
phase must convey Denver, clear weather, 62 degrees and its historical/recorded
qualification in both the public response and actual completed playback segment
text. A generic “Done” receipt or presenting the fixture as current weather fails.

`completion-boundary.json` retains phases, public events, generation entry/cancel
times, exact scoped pending/communicated evidence and failures. `weather-source.json`
retains immutable recorded data and its source timestamp/hash separately from
actual admission/publication times. Raw/processed microphone WAVs, ordered
frontend callbacks and per-stimulus timing/alignment remain beside the report.
These are controlled coordinator-boundary injections, not evidence of live
weather retrieval, worker inference, or current external forecast accuracy.

### Interruption with real worker load

`interruption-worker.yaml` surrounds the same physical interruption control with
an actual sustained local Agency worker and scoped cancellation/join. Successful
provider requests must occur before the stimulus and overlap the interruption
window; a running job label or a stalled HTTP call cannot establish worker load.
Request content is not recorded. Readiness and post-measurement progress have
30-second bounds, followed by 10-second cleanup. These are measurement availability
limits, separate from detector-to-stop latency.

Use the same explicit config for both conditions, with the worker's independent
Ollama endpoint/model selected in that file. Prepare and verify the owned worker
endpoint before this recipe; it never starts or changes a daemon. No build,
other model diagnostic, or competing hardware owner may overlap collection.

```sh
just test acoustic-interruption-pair CONFIG SUITE RUNTIME_JSON NEW_OUTPUT
```

`RUNTIME_JSON` is a JSON object mapping descriptive asset names to absolute file
paths. Include the selected STT/TTS model files and loaded native libraries. The
runner hashes these declared assets and checks them before/after every child;
undeclared environment state is outside its fingerprint. The same binary,
config, fixture content, control timing, actual voice engine and negotiated
routes must be present in both populations. Both require three excluded warmups
and 30 measured observations. Fresh processes do not prove model coldness.

The comparison revalidates retained child artifacts and computes absolute
nearest-rank p50/p95 from measured detector-to-stop durations. It also reports
worker-minus-baseline deltas. To avoid treating the contract's 50ms allowance as
p95-only, its conservative gate uses the slowest measured worker stop minus the
fastest measured baseline stop. Passing bounds every observed cross-condition
difference; it does not guarantee future latency or measure speech-onset delay.
Any failed attempt stops the population and retains its artifacts without passing
quantiles, retries, or sample replacement.

### Recording off/on terminal population

`just test capture-terminal NEW_ROOT CONFIG WAV WORK PAIRS SEED` drives the
actual terminal App with recorded WAV input through real VAD/STT and the configured
brain/TTS. It requires an explicit named output device and playback gain zero;
it fails before input release if native output identity or gain cannot be verified.
Use a dedicated non-forwarding virtual sink. Do not change the system default or
mute user audio. This measures software playback boundaries, not acoustic onset.

Two predefined warmups precede seeded counterbalanced pairs. Each attempt resets
conversation history behind the input gate; all observations, failures, effective
configuration, binary/WAV hashes, device negotiation and capture IDs are retained.
The same capture can span adjacent recording-on attempts. Failed or incomplete
populations remain evidence and cannot silently supply replacement samples.

After the population finishes, analyze it without starting providers:

```sh
go run ./tests/convoflow -capture-overhead POPULATION_ROOT
```

This comparison is distinct from the existing baseline-versus-worker comparison.
It measures incremental recording cost on one instrumented binary using
finalized-input to first native software playback. It reports nearest-rank off/on
quantiles, paired deltas and a seeded pair-resampled bootstrap confidence interval.
Passing requires both empirical p95 regression and its upper confidence bound to
fit `max(10ms, 2% of recording-off p95)`. A noisy population remains inconclusive;
retain it and predeclare any additional population before collecting more data.

Provider diagnostics must accompany the numbers. In the OV0007 stabilized local
recovery workload, reported token usage omits separate continuity-compaction
requests. An observable response below a configured token cap does not prove those
internal requests were uncapped or complete. Preserve that blind spot rather than
attributing the whole delay to uncomplicated model inference.
