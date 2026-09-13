# Voice runtime code layout

The voice pipeline, Ollama provider, config package, Qwen TTS provider, audio
player, meeting log and benchmark command use files grouped by responsibility
within their existing Go packages. Their public interfaces and state ownership are
unchanged. This guide identifies where to make a change without tracing a single
large implementation file.

## Pipeline

All paths below are in [`pkg/voiceagent/pipeline`](../pkg/voiceagent/pipeline).

| File | Responsibility |
| --- | --- |
| `pipeline.go` | `Pipeline` dependencies/state, capture interfaces, output controls, TTS replacement and echo reset |
| `voice_turn.go` | Voice-turn entry point and transcription flow |
| `text_turn.go` | Text/background-turn execution and text completion |
| `turn_deadline.go` | Model/tool idle deadlines and activity-based deadline extension |
| `turn_metrics.go` | Timing snapshots, segment accounting, audible-sink identity and first-audio stamps |
| `turn_conductor.go` | Terminal turn transitions, metrics publication and keep-up advice |
| `response_stream.go` | Stream coordination, response collection and model-stream observation |
| `synthesis.go` | Segment synthesis, fallback eligibility, silence bounds and stream disposal |
| `playback.go` | Playback events, lifecycle observation and synthesis/playback timing stamps |
| `speech_recovery.go` | Recovery text, best-effort speech and standalone announcements |
| `speech_content.go` | Speakable-content filtering and voice batching constants |
| `brain_hooks.go` | Model/tool/usage hooks, route context and event translation |
| `compaction.go` | Turn preparation and conversation compaction |

Existing collaborators remain in their established files: routing in `route.go`,
delegation in `agency.go`, narration in `narrate.go`, background turns in
`background.go`, retained capture in `transcription.go`, and interruption control
in `interrupt.go`. Consult those files before adding another implementation of
the same responsibility.

`Pipeline` still owns the shared state and synchronization. The turn conductor
still publishes terminal metrics; the stream loop still coordinates synthesis,
playback and interruption. Moving a method into a focused file does not make it
safe to invoke from a different goroutine. Preserve its caller/lock contract.

## Ollama provider

All paths below are in [`pkg/voiceagent/brain`](../pkg/voiceagent/brain).

| File | Responsibility |
| --- | --- |
| `ollama.go` | Provider state, construction, info sink and factory registration |
| `ollama_stream.go` | Streaming inference and chunk emission |
| `ollama_blocking.go` | Blocking inference and its tool loop |
| `ollama_request.go` | Chat request/options construction and server capability negotiation |
| `ollama_think.go` | Thinking settings, warnings and thinking-only response handling |
| `ollama_warmup.go` | Bounded provider warmup |
| `ollama_history.go` | Speaker attribution, history persistence/restoration, fresh providers and history-window hygiene |
| `ollama_context.go` | Context-budget accounting, old-message eviction and tool-output shrinking |
| `ollama_prompt.go` | Skills catalog, system-prompt assembly, request messages and skill routing |

`OllamaBrain` remains the state owner. Streaming and blocking paths still share
the same request, history and context-budget helpers. Provider registration stays
in `ollama.go`; moving files must not change initialization ordering or introduce
new registration side effects.

## Config

All paths below are in [`pkg/voiceagent/config`](../pkg/voiceagent/config).

| File | Responsibility |
| --- | --- |
| `config.go` | Package state (`configDir`, `configFile`, the viper instance, `mu`), viper init and the get/set/save accessor surface |
| `types.go` | `Config` and its nested meeting and speaker structs |
| `defaults.go` | Shipped defaults and the value bounds applied at load |
| `env.go` | Environment bindings, aliases and the resolution order the loader honors |
| `load.go` | Load/unmarshal lifecycle and the after-load persona hook |
| `ollama_defaults.go` | Ollama capability auto-enable and the brain-provider switch |
| `paths.go` | Install-root directory resolution (models, sessions, meetings, personas, prompts, skills) |

Model-asset install is the package's other large surface, grouped the same way:

| File | Responsibility |
| --- | --- |
| `models.go` | Asset descriptor types, phase vocabulary, progress arithmetic |
| `download.go` | HTTP fetch, stall-timeout guard, checksum copy core |
| `extract.go` | Tar extraction, prefix stripping, path-traversal rejection |
| `archive_marker.go` | On-disk verification and the install-marker record |
| `ensure.go` | Public `Ensure*` surface and the manifest walk |
| `ensure_native.go` | Native Qwen3-TTS tier install, stage to phase mapping |

Existing collaborators remain in their established files: the settings schema in
`schema*.go`, surgical key writes in `set*.go`, and per-domain keys in `stt.go`,
`worker.go`, `brains.go`, `workdir.go` and the rest. Consult those files before
adding another implementation of the same responsibility.

`mu` still guards the package's viper instance and not the fields of a `*Config`
a caller already holds — the invariant is recorded on the `mu` declaration.
Moving an accessor into a focused file does not change which lock its caller
must hold.

## Qwen TTS provider

The Qwen provider is its own package: [`pkg/voiceagent/tts/qwen`](../pkg/voiceagent/tts/qwen).
It imports the contract in `pkg/voiceagent/tts`, and nothing in `tts` imports it back, so the
boundary is checked by the compiler instead of by convention.

| File | Responsibility |
| --- | --- |
| `qwen.go` | Tuning constants, the worker process seam, `Qwen3TTS` state, constructors and lifecycle |
| `policy.go` | Speech place, model tier, streaming and warm-state policy |
| `grace.go` | First-audio grace window and its ceilings |
| `request.go` | `Synthesize`, `SynthesizeRequest`, `SoftCancel`, reference validation |
| `stream.go` | Native and managed stream synthesis, session ensure/discard under `sessionMu` |
| `worker.go` | Synthesis dispatch, external CLI path, WAV streaming, argument building |
| `capabilities.go` | `ListVoices`, `Capabilities`, `Status`, shipped-clone binding |
| `conn.go`, `session.go`, `native_session.go`, `remote_session.go`, `speech_session.go` | Transport and session plumbing, unchanged by the move |

Selection lives in [`pkg/voiceagent/tts/providers`](../pkg/voiceagent/tts/providers), above
both. It has to: a provider package imports the contract, so the contract cannot import
providers back. Putting `NewProvider` in its own package means a provider can only go missing
by editing that file — not by forgetting a blank import, which would compile and then fail at
runtime on a machine that had the model installed. Add a provider there or it does not exist.

Two consequences worth knowing before moving anything else:

- `SpeechCharsPerSecond` and `TypicalSegmentChars` are exported from `provider.go`. They sit
  beside the `SegmentGracer` interface because every provider implementing it needs them, and
  providers no longer share a package.
- `NewAudio8Provider` owns Audio8's model-directory assembly. The selector used to know that
  layout; now only the package that owns Audio8 does.

Kokoro and Audio8 are still in `pkg/voiceagent/tts` itself. They cannot move the same way yet:
the core reads Kokoro's unexported `voiceNames`, `genderMap` and `langMap` from three files.
Moving each provider out means relocating what the core reads from it first.

## Audio player

All paths below are in [`pkg/voiceagent/audio`](../pkg/voiceagent/audio).

| File | Responsibility |
| --- | --- |
| `player.go` | Tuning constants, `Playback`/`Engine` types, `Player` state and its lock docs, public control surface |
| `player_pump.go` | Producer goroutine, buffered and incremental pump loops, stall silence timer |
| `player_segment.go` | `playbackSegment` ring buffer, the `s.mu` contract, readiness gating, underrun accounting |
| `player_device.go` | Device open, channel and rate negotiation, AEC reference-delay estimate |
| `player_callback.go` | The miniaudio callback thread: `onData`, queue advance, channel expand |
| `player_pcm.go` | Stateless PCM buffer helpers |

This package is the one place where a file boundary can hide a thread contract, so
some groupings are deliberate and should not be split further. `player_callback.go`
holds the callback thread together with the only two functions that take `p.mu` from
it. `playbackSegment` stays whole because its `…Locked` family is only correct read
beside the callers that hold `s.mu`, and its writers span threads: `writeTo` on the
callback, `append`/`finishInput` on the pump, and `interrupt` on *both* — the caller
through `PlayStream`/`Stop`, and the callback itself when `PlaybackAttempt.Write`
fails on an invalidated attempt. Do not record `interrupt` as caller-only; that the
callback can interrupt a segment it is mid-write on is the sharpest reason these
declarations belong in one file. `startPlaybackDevice` stays beside `ensureDevice` because `Device.Start` can
invoke `onData` synchronously, so holding `p.mu` across it deadlocks.

## Meeting log

All paths below are in [`internal/meeting/log`](../internal/meeting/log).

| File | Responsibility |
| --- | --- |
| `log.go` | `Writer` state, bundle create/abort, accessors, `Close`, the append primitives |
| `events.go` | `ReadEvents`, torn-tail recovery, attributed-transcript merge |
| `speakers.go` | Speaker analysis writing, offset formatting, post-`Close` reopen |
| `types.go` | Event kinds, bundle filenames, `Event`, `TranscriptRecord`, `Summary` |
| `capture.go` | `listen.Sink` implementation, notes and bookmarks |
| `resume.go` | `OpenBundle` and event-time parsing |
| `control.go` | Control symbols, `AppendControl`, route-plan writing |

## Benchmark command

All paths below are in [`cmd/obey-voice/cmd`](../cmd/obey-voice/cmd). Every file carries
the `//go:build !integration` constraint the command has always had.

| File | Responsibility |
| --- | --- |
| `benchmark.go` | Flag variables and their value types, TTS place, output resolution, the cobra command and `init()` |
| `benchmark_pipeline.go` | Text-prompt and full-turn fixture modes, which both build a pipeline |
| `benchmark_stt.go` | STT fixture mode, which builds VAD and the STT provider directly |
| `benchmark_result.go` | Result model, event-bus drain, thresholds, exit status, transcript scoring |
| `benchmark_report.go` | Console summary and JSON artifact |

## Maintaining the boundaries

Put stage-specific behavior beside its existing owner. Keep lifecycle/locking
changes separate from declaration moves so reviewers can assess behavior changes
directly. Avoid catch-all helper files and unnecessary interfaces between files
in the same package. Keep a cohesive control loop intact when splitting it would
obscure its cancellation or event-ordering guarantees.

Existing tests retain their locations and names. For changes to these packages,
run the repository lint gate and affected-package race tests, followed by the
repository test suite:

```sh
just lint
go test -race ./pkg/voiceagent/pipeline ./pkg/voiceagent/brain ./pkg/voiceagent/config \
  ./pkg/voiceagent/tts ./pkg/voiceagent/audio ./internal/meeting/log ./cmd/...
go test ./...
```

Real microphone/model smoke checks remain opt-in as described in
[Development](development.md). A same-package file split does not establish new
audio-performance or concurrency guarantees. For `pkg/voiceagent/audio` and the TTS
providers that means the hardware pass is still owed separately: the default lane
never opens a real device and never starts a real Qwen worker.
