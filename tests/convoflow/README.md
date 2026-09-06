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

Playback is captured by a recording `audio.Engine` (no speakers unless
`-speakers`). Each turn writes `audio/<nn>-<id>/playback.wav` and
`analysis.json`. `just convoflow analyze RUN_DIR` rescores those WAVs.

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
