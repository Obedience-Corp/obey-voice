# convoflow

Opt-in conversation harness for the live-router / worker split. **Not** part of
`just test` or CI.

Default path is local: one Ollama daemon, live brain + worker as two clients of
the same model. Edit knobs at the top of `.justfiles/convoflow.just` or pass
them on the command line.

```bash
just convoflow                         # list recipes
just convoflow run                     # search-web-intent, justfile defaults
just convoflow run live_model=qwen3.8:27b
just convoflow gate                    # three Layer-1 scenarios (v1 success)
just convoflow live-router             # soft live-brain spawn
just convoflow run worker_provider=grok   # comparison, not the gate
```

Recordings land in `~/.cache/obey-voice/convoflow/<scenario>-<ts>/` (override
`OBEY_VOICE_CONVOFLOW_DIR`).

This skeleton loads YAML, asserts turn **and** scenario-level expects, and
wires the just module. Live `voiceagent.New` construction is a follow-up; until
then `just convoflow run` exits 2 after writing the run dir.

Playback is captured by a recording `audio.Engine` (no speakers unless
`-speakers`). Each turn writes `audio/<nn>-<id>/playback.wav` and
`analysis.json`. `just convoflow analyze RUN_DIR` rescores those WAVs.

The no-model path is a dedicated fixture, not the stock gates:

```bash
go run ./tests/convoflow -self-test -scenario tests/convoflow/scenarios/self-test.yaml
```

`-self-test` refuses any other scenario. `go test ./tests/convoflow` needs no models.

Gate YAMLs assert `crackle_failing_wavs == 0` once playback WAVs are scored.
Asserting that metric without a recording player still fails closed.
