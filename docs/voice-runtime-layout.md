# Voice runtime code layout

The voice pipeline, Ollama provider and config package use files grouped by
responsibility within their existing Go packages. Their public interfaces and state ownership are
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

Existing collaborators remain in their established files: the settings schema in
`schema*.go`, surgical key writes in `set*.go`, and per-domain keys in `stt.go`,
`worker.go`, `brains.go`, `workdir.go` and the rest. Consult those files before
adding another implementation of the same responsibility.

`mu` still guards the package's viper instance and not the fields of a `*Config`
a caller already holds — the invariant is recorded on the `mu` declaration.
Moving an accessor into a focused file does not change which lock its caller
must hold.

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
go test -race ./pkg/voiceagent/pipeline ./pkg/voiceagent/brain ./pkg/voiceagent/config
go test ./...
```

Real microphone/model smoke checks remain opt-in as described in
[Development](development.md). A same-package file split does not establish new
audio-performance or concurrency guarantees.
