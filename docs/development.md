# Development

[Back to README](../README.md)

```bash
just              # Show available commands
just build        # Vet and compile using the build dashboard
just run -- --text
just talk         # Full voice mode
just lint         # Format, vet, and run configured linters
just deps         # Update and tidy dependencies
```

The build dashboard is wired through `internal/buildutil` and the project keeps using that workflow.

See [Voice runtime code layout](voice-runtime-layout.md) for file responsibilities
and state ownership across the pipeline, Ollama provider, config package, Qwen TTS
provider, audio player, meeting log and benchmark command.

## Testing

```bash
just test unit                 # Unit tests
just test pkg config           # Test a specific internal package
just test integration          # Container integration tests
just test integration-verbose  # Integration tests with full output
just test audio-crackle        # Playback layout + crackle software regressions (CI-safe)
just test audio-hardware       # Opt-in: real speakers, Studio Display etc.
go test ./...                  # Plain Go test fallback
```

Integration tests expect `bin/linux/obey-voice` to exist. The build dashboard creates it for the integration workflow.

Playback crackle (Studio Display mono-client class) is guarded by `pkg/voiceagent/audio`
layout + crackle tests in normal `go test -race ./...`. After any change under
`pkg/voiceagent/audio`, also run `just test audio-hardware` on an affected machine and
confirm `--debug-audio` metadata reports `channels: 2` (not mono).

### Voice smoke tests (opt-in, require local models)

The STT provider loops (`pkg/voiceagent/stt`) are covered by deterministic unit tests
that use fakes, so they run without model files. Real end-to-end voice behavior
depends on local STT/VAD/TTS models and is therefore opt-in. When the models are
installed (`obey-voice models ensure`, once available), run the smoke plan:

| Scenario | Expectation |
|----------|-------------|
| Short utterance (`hello samantha`) | final transcript within ~2s; finalizes on the source's EOF/silence, not a phrase timeout |
| Long utterance | partial/final transcript; caps at the max-utterance length |
| Silence only | times out with no final transcript |
| Finite fixture EOF | terminates promptly on the explicit final frame, no hang |

```bash
# Deterministic, no models needed:
go test ./pkg/voiceagent/stt ./pkg/voiceagent/endpoint ./pkg/voiceagent/audio ./pkg/voiceagent/transcription

# Pipeline flow with stubbed stages (no models, no network):
go test -tags integration ./tests/voiceflow      # turn state machine + barge-in

# Real-provider smoke (needs models + whisper.cpp binary for that provider):
just qwen-live                                   # real native Qwen voices + cancel/restart WAVs
obey-voice listen                                  # manual: speak a short command
```

### File transcription (opt-in, require local models)

`obey-voice transcribe` is covered without models by command-level tests with
injected dependencies and by process-level tests that run the real binary with
an isolated `HOME` and an empty `MODELS_DIR`. Two checks need real models; the
SIGINT test is env-gated:

```bash
# Interrupt a real run and measure the time to exit 130 and a cancelled event:
OBEY_VOICE_TRANSCRIBE_MODELS_DIR="$HOME/.cache/festival-voice/models" \
OBEY_VOICE_TRANSCRIBE_LONG_WAV=/absolute/path/to/60s-16k-mono.wav \
  go test ./cmd/obey-voice/cmd -run TestTranscribeProcessCancellation -count=1 -v

# The independent shell client against the configured model (read-only; no downloads):
docs/transcribe-protocol/client-example.sh /absolute/path/to/speech.wav
```

Regenerate the conformance goldens after a deliberate protocol change:

```bash
go test ./cmd/obey-voice/cmd -run TestTranscribeConformanceFixtures -update-transcribe-golden
```

and copy them into `docs/transcribe-protocol/fixtures/`, which other clients
read.

### File transcription checks (opt-in, require local models)

`pkg/voiceagent/transcription` runs on fakes in the default lane: the runner, the
span math and the WAV reader have no model or device dependency. Three checks do
need the installed models and are gated behind their own variables, so a machine
without them still passes the suite.

| Check | Variable | What it proves |
|---|---|---|
| `TestFileVADKeepsOffsetsAbsoluteAcrossTheBuffer` and `TestFileVADSplitsLongSpeechIntoAdjacentSpans` (`pkg/voiceagent/audio`) | `OBEY_VOICE_FILE_VAD_MODELS_DIR` | Native VAD span starts stay absolute past the 30 s detector buffer and across a forced split |
| `TestRunPlacesRealSpeechOnTheFileTimeline` (`pkg/voiceagent/transcription`) | `OBEY_VOICE_TRANSCRIBE_MODELS_DIR` | Two real utterances land within 500 ms of their known positions in the file |
| `TestT3bTranscribeFixture` (`cmd/obey-voice/cmd`) | `T3B_FIXTURE`, `T3B_OUT` | The existing legacy meeting replay still transcribes the fixture through `newReplaySource` and `replayTranscribe`, and dumps the records for comparison |

```bash
# Deterministic, no models needed:
go test -race ./pkg/voiceagent/transcription ./pkg/voiceagent/audio ./pkg/voiceagent/stt

# Real models, pointed at the installed directory:
OBEY_VOICE_TRANSCRIBE_MODELS_DIR="$(obey-voice paths --json | jq -r .models_dir)" \
  go test ./pkg/voiceagent/transcription -run TestRunPlacesRealSpeechOnTheFileTimeline -v
```

`just test pkg` only reaches `./internal/`, so run the transcription package by
path. The two-hour bounded-memory proof in `pkg/voiceagent/audio` is skipped
under `-short`.

### Conversation flow gate (opt-in, local Ollama live + worker)

`tests/convoflow` drives a production `voiceagent.Agent` through a scripted
conversation and keeps the recording. It is the check that "search the web for
X" spawns on the **local** worker instead of chatting that search is
unavailable: one Ollama daemon, live brain and worker as two clients of the same
model, real `legacy` tools on the worker, real TTS scored for crackle. Not part
of `just test` or CI; it needs a running Ollama with the model pulled and the
TTS assets installed, and exits 2 when they are missing.

```bash
just convoflow                                              # list the module
just convoflow gate                                         # v1 success: three Layer-1 scenarios
CONVOFLOW_LIVE_MODEL=qwen3.8:27b just convoflow gate        # same, bigger local model
just convoflow run search-web-intent -live-model=qwen3.8:27b   # one scenario, flags override knobs
just convoflow live-router                                  # soft: the live brain may route(spawn) or chat
just convoflow run search-web-intent -worker-provider=stub  # self-test worker; gate refuses it
just convoflow analyze RUN_DIR                              # rescore a kept run
```

Runs land in `~/.cache/obey-voice/convoflow/<scenario>-<ts>/` with the resolved
knobs, every bus event, `metrics.json`, and a `report.md`. Knobs live at the top
of `.justfiles/convoflow.just`; see `tests/convoflow/README.md`.

### Latency benchmarks (protect the sub-2s goal)

The `obey-voice benchmark` command measures the perceived-latency milestones that
protect the <2s end-to-end goal and emits them as both a summary and (with
`--json`) a stable `TurnMetrics` record per turn: STT final, first model chunk,
first segment, first audio ready, playback start, playback complete, and — on a
barged-in turn — interruption latency. Threshold flags fail the run when a
milestone regresses, so the benchmark can gate CI or a local check:

```bash
# Prompt latency with budgets (any breach exits non-zero):
obey-voice benchmark --prompt "hello" \
  --max-total 2s --max-first-model-chunk 500ms --max-playback-start 800ms

# STT fixture latency + transcript accuracy (stops at the final transcript):
obey-voice benchmark --audio-fixture utterance.wav --expect-text "hello there" \
  --max-stt-final 2s --min-transcript-score 0.8

# Full voice turn from a recording — capture, VAD, STT, brain, TTS, playback:
obey-voice benchmark --full-turn-fixture utterance.wav --expect-text "hello there" \
  --max-playback-start 1200ms

# Machine-readable output for tracking regressions over time:
obey-voice benchmark --prompt "hello" --json bench.json
```

The three modes measure different paths, and only one of them measures the voice
pipeline end to end:

| Flag | Path | Use it for |
|---|---|---|
| `--prompt` (default) | brain → TTS, **non-streaming** — the whole reply is synthesized as one segment | Text-path regressions |
| `--audio-fixture` | capture → VAD → STT, stops at the final transcript | STT latency and accuracy |
| `--full-turn-fixture` | the real `RunTurn` path, end to end | Anything touching endpointing, sentence chunking, or time-to-first-audio |

`--full-turn-fixture` is the one to use when comparing VAD, STT-provider or
first-sentence changes: it is the only mode where `FirstSegmentElapsed` reflects
real sentence chunking rather than end-of-generation.

Interruption latency is reported only when a turn is interrupted; all milestones
are always present in the `--json` output.

Committed before/after artifacts live in [benchmarks/](benchmarks/README.md).

### Diarization A/B (T3 / T3b)

`obey-voice benchmark` measures the conversation turn; it cannot see the offline
meeting review pass, so the optional
[Sortformer diarizer](configuration.md#sortformer-meeting-diarizer-optional) has
its own recipe:

```bash
just fetch-meeting-fixture               # shared 90s multi-speaker WAV (gitignored)
scripts/diar-ab-sortformer.sh --help     # prereqs: CLI, GGUF, installed engine
scripts/diar-ab-sortformer.sh            # -> docs/benchmarks/T3-diar-ab-sortformer-<date>.json
```

The script runs both engines over the same fixture, then aggregates the raw
outputs into the `obey-voice.bench.diar-ab.v1` artifact with
`scripts/diar_ab_aggregate.py`; every path it needs is overridable by
environment variable, and it prints the prereq block on a missing input. The
attribution follow-up (does a better timeline change who said what) is
env-gated Go tests over committed inputs —
[`benchmarks/t3b/README.md`](benchmarks/t3b/README.md) has the two commands, and
steps 2–3 reproduce with no models installed.

## Model assets and readiness

Local model assets are described by an asset manifest and managed by three
commands:

```bash
obey-voice models status        # read-only: which assets are installed vs missing
obey-voice models status --json # machine-readable for scripts
obey-voice models ensure        # download any missing assets (atomic + verified)
obey-voice doctor               # diagnose config, assets, and external binaries
```

`models status` and `doctor` are read-only and safe offline. Doctor validates
the selected Claude/Grok CLI or Ollama configuration without making a network
request. The exceptions are a speech server: with `tts_provider: openai-speech`
both ask it what it serves (bounded to 3 s), and with `tts_place: lan` doctor
dials the speech worker; a server that is down is reported, never a crash.
Doctor exits non-zero on setup errors (a missing model asset remains a
warning that points you to `models ensure`). Downloads are reliable by
construction: each file is written to
a temp file, size/checksum-verified when known, and atomically renamed; archives
are extracted into a temp directory, verified, then promoted — so an interrupted
or corrupt download never lands a partial asset, and **re-running
`models ensure` cleanly recovers**.

### Cleaning unused assets

`models clean` never removes anything it has not shown you, and never treats an
asset your configuration or any persona references as unused — the required set
is the union of the global config, every persona profile, and every config key
that names an asset (including ones the active mode does not load, such as
`sherpa_streaming_model` while `stt_mode: offline`). If that set cannot be
resolved — an unreadable persona, an unsupported model name — the command exits
non-zero and deletes nothing.

```bash
obey-voice models clean --unused --dry-run          # list candidates + "Kept (N)" with reasons
obey-voice models clean --unused --dry-run --json > plan.json
obey-voice models clean --unused --yes --plan plan.json --json
```

| Flag | Meaning |
| --- | --- |
| `--unused` | Required. Only unused-asset cleanup is supported. |
| `--dry-run` | List what would be removed and what is kept, with reasons. Deletes nothing. |
| `--yes` | Delete. Exactly one of `--dry-run` or `--yes` is required. At a terminal it prints the list and asks for confirmation. |
| `--plan <file\|->` | Apply exactly the reviewed plan: the whole `--dry-run --json` document (a bare `plan_id` is refused — it does not name the install it came from). Required with `--yes` whenever there is no terminal to confirm at, and always with `--yes --json`. |
| `--json` | Machine-readable output (`schema_version: 2`). |

The dry-run JSON carries `candidates[]` (with `size_bytes`, `category:
junk|asset`, `kind`), `protected[]` (each kept path with the persona or config
key that keeps it), `total_bytes`, and `plan_id`. An apply recomputes the
candidate set and refuses when it no longer matches the plan — or when the plan
was captured against a different `models_dir` — printing
`{"error":"plan_changed","plan_id":…,"current_plan_id":…}` and deleting
nothing; re-run the dry run and review the new list. A successful apply reports
`deleted[]`, `skipped[]` (with a reason) and `bytes_freed`. Every other `--json`
failure arrives on stdout as `{"error":"required_assets|plan_invalid|delete_failed",
"message":…}`, so a front end never has to parse a banner off stderr.

Automated tests cover download/extraction reliability with fake HTTP servers (no
network). To verify the **real** assets manually:

```bash
obey-voice models status        # confirm what's missing
obey-voice models ensure        # download from the real release URLs
obey-voice doctor               # confirm everything reports OK
```

## Voice utilities

```bash
just voice test
just voice voices
just voice providers
```
