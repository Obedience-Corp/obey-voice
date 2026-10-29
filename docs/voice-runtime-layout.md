# Voice runtime code layout

The voice pipeline, Ollama provider, config package, audio player, meeting log,
benchmark command, settings rows, the TUI's App update loop and the conversation
screen use files grouped by responsibility within their existing Go packages;
the Qwen TTS provider went further and became its own package. This guide
identifies where to make a change without tracing a single large implementation
file, and records the contracts a file boundary could otherwise hide.

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
| `response_stream.go` | Stream coordination, segment grouping and the idle flush, response collection and model-stream observation |
| `synthesis.go` | Segment synthesis, fallback eligibility, silence bounds and stream disposal |
| `playback.go` | Playback events, lifecycle observation and synthesis/playback timing stamps |
| `speech_recovery.go` | Recovery text, best-effort speech and standalone announcements |
| `speech_content.go` | Speakable-content filtering, the segment-grouping policy and the textual sentence join |
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
- `SegmentRamp` and `SentenceJoiner` are optional capabilities in the same file. They describe
  how fast segments may grow and whether a request join carries prosody; the pipeline reads them
  through `SegmentRampOf` / `JoinsSentencesOf`, which supply the defaults for a provider that
  declares neither.
- `NewAudio8Provider` owns Audio8's model-directory assembly. The selector used to know that
  layout; now only the package that owns Audio8 does.

Kokoro and Audio8 are still in `pkg/voiceagent/tts` itself. They cannot move the same way yet:
the core reads Kokoro's unexported `voiceNames`, `genderMap` and `langMap` from three files.
Moving each provider out means relocating what the core reads from it first.

## Audio player

The device-backed player is its own package: [`pkg/voiceagent/audio/playback`](../pkg/voiceagent/audio/playback).
`audio` keeps the playback *contract* — `Engine`, `Playback`, `PlaybackResult`, `PlaybackAttempt` —
so the code that implements or consumes it (the netapi fanout, the convoflow recorder, the
pipeline) stays where it is; only code constructing a concrete `Player` imports `playback`.
Measured with `go/types`, nothing in `audio` references `playback`.

| File | Responsibility |
| --- | --- |
| `player.go` | Tuning constants, `Player` state and its lock docs, constructors, public control surface |
| `pump.go` | Producer goroutine, buffered and incremental pump loops, stall silence timer |
| `segment.go` | `playbackSegment` ring buffer, the `s.mu` contract, readiness gating, underrun accounting |
| `device.go` | Device open, channel and rate negotiation, AEC reference-delay estimate |
| `callback.go` | The miniaudio callback thread: `onData`, queue advance, channel expand |
| `pcm.go` | PCM conversion helpers |
| `aec_ref.go` | AEC reference ingress for the frontend tap |
| `volume.go` | Playback volume, applied in the callback |
| `debug.go` | The player's debug recorder and WAV writer |
| `crackle.go` | Crackle/underrun analysis of playback output |

This package is the one place where a file boundary can hide a thread contract, so some
groupings are deliberate and should not be split further. `callback.go` holds the callback
thread together with the only two functions that take `p.mu` from it. `playbackSegment` stays
whole because its `…Locked` family is only correct read beside the callers that hold `s.mu`,
and its writers span threads: `writeTo` on the callback, `append`/`finishInput` on the pump,
and `interrupt` on *both* — the caller through `PlayStream`/`Stop`, and the callback itself
when `PlaybackAttempt.Write` fails on an invalidated attempt. Do not record `interrupt` as
caller-only; that the callback can interrupt a segment it is mid-write on is the sharpest
reason these declarations belong together. `startPlaybackDevice` stays beside `ensureDevice`
because `Device.Start` can invoke `onData` synchronously, so holding `p.mu` across it deadlocks.

Six helpers are exported from `audio` because capture and playback genuinely share them:
`SelectDevice`, `ChoosePlaybackChannels`, `PlaybackDeviceDetails`, `PickPlaybackFormat`,
`ResampleLinear` and `StreamResampler`. Reach for those before adding a second implementation
on either side of the boundary.

## File transcription

Turning a finished recording into timed captions is its own package:
[`pkg/voiceagent/transcription`](../pkg/voiceagent/transcription). It sits above
`audio` and `stt` and imports both; neither imports it back, so the boundary is
checked by the compiler. It is the reusable finite-audio core that a later
command or adapter can call: a run reads audio, finds speech spans, decodes
each one and reports it with the sample positions it came from. It holds no
conversational behavior at all: no brain, no TTS, no routing, no history, no
spoken stop commands, no conversational duration limits.

| File | Responsibility |
| --- | --- |
| `doc.go` | The package comment: what this package is for, and the never-clear rule stated once |
| `types.go` | The request, the injected dependencies, the closed event set, the outcome values and the typed `*Error` with its stable codes |
| `timing.go` | Sample-to-microsecond conversion and the span interval invariant |
| `spans.go` | `Span`, `SpanDetector`, the VAD adapter, the bounded pre-roll ring, and `NewFileSpanDetector`, the one constructor every file run uses |
| `runner.go` | `Run`: the read loop, the min-speech and audibility gates, decode, segment emission, progress throttling and the end-of-file timeline check |

Two rules hold this package together, and both are cheap to break by accident.

**Never clear the detector while a file is in flight.** `audio.VAD.Clear()` calls
the native `Reset()`, which zeroes the sample-offset origin, so every span start
after a clear is measured from the wrong place. The live conversational loops
reset between turns and are right to; this one runs one detector for one file
from beginning to end. `audio.NewFileVAD` exists to make that configuration
explicit rather than reusing `NewVAD` and hoping.

**Count the audio three times and compare.** The runner keeps its own sample
counter, the source reports `Position()`, and the detector reports `Fed()`. They
must agree when the file ends. A disagreement means every caption after the
divergence is wrong by that amount, so it fails the job instead of shipping
plausible-looking times.

A recognizer joins by implementing `stt.SegmentDecoder`: one exported
`DecodeSegment` method delegating to the transcribe function it already has, one
row in `stt`'s spec table declaring `SegmentDecode` and its `Timing` values, and
one branch in `stt.NewSegmentDecoder`. There is no second model registry:
selection, normalization, asset resolution and the correction chain are the same
ones `stt.NewProvider` uses. A provider that cannot decode a finite span returns
`stt.ErrSegmentDecodeUnsupported` rather than being absent.

The import boundary is a test, not a convention: `boundary_test.go` asks
`go list` what the package imports and fails if anything other than `audio` and
`stt` appears. Note that `pkg/voiceagent/stt` imports `pkg/voiceagent/brain`
itself, for the optional model-based transcript correction stage, so the
transitive graph is not the place to look for that rule.

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

## Settings rows

All paths below are in [`internal/tui`](../internal/tui). Every settings section is a list of
`settingRow` values rebuilt from the live config each draw; `sectionRows` in `settings_rows.go`
dispatches by section to one file per settings domain. The kinds, `settingRow`, the `applies*`
constants, `indexOf` (used by every domain) and `onOff` (used package-wide, from `settings_place.go`,
`settings_qwen.go` and `workers.go` too) stay in `settings_rows.go`. Domain-only helpers sit
beside their single caller.

| File | Responsibility |
| --- | --- |
| `settings_rows.go` | `rowKind` and its docs, the `applies*` constants, `settingRow`, `onOff`, `indexOf`, `sectionRows` |
| `settings_rows_brain.go` | Brain section: provider and model rows |
| `settings_rows_tools.go` | Tools section: skills, voice tools, barge-in, name summon, AEC front end, mouse and talk-key rows |
| `settings_rows_worker.go` | Worker section: legacy and tier rows with the budget, billing and ref helpers only they use |
| `settings_rows_voice.go` | Voice and Qwen sections: TTS provider, voice, language and Qwen rows |
| `settings_rows_speech.go` | STT and Speakers sections, with `nearestIndex32` |
| `settings_rows_audio.go` | Audio section: input and output device rows, the playback volume slider, `deviceOptions` |
| `settings_rows_meeting.go` | Meeting section |

## App update loop

All paths below are in [`internal/tui`](../internal/tui). `App.update` in `tui.go` is the single
dispatch point: a `switch msg := msg.(type)` whose every arm calls one named `on…` handler, then
`return a.delegate(msg)`. Handlers are methods on `App` with the same value receiver as `update`,
so each one returns the `App` it mutated. Two handlers fall through rather than return: `onKey`
returns `(App, tea.Cmd, bool)` with the bool meaning "returned", and `onWindowSize` returns the
`App`; both are visible as such at their `case`. Keep the switch as the only dispatch: no handler
map, no interface, and `returnToMenuMsg` still re-enters through `a.update(quitMsg{})`.

| File | Responsibility |
| --- | --- |
| `tui.go` | `App`, constructors, message types, `Update`/`update` dispatch, runtime selection sync, `View` |
| `tui_screens.go` | Key and resize handling, screen switches, book/library transitions, quit, and `delegate`: the hidden-conversation delivery block and the per-screen `Update` fan-out |
| `tui_meeting.go` | Meeting lifecycle messages: start, ready, done, results, route, analysis, sweep |
| `tui_runtime.go` | Pipeline build and runtime-ready, settings saved, microphone switch, voice reload |
| `tui_talk.go` | Persona selection, talk hotkey, and work-projection polling |

`delegate` is the fall-through target and must stay after the switch, not inside a handler: a
handler that ends by delegating would hide which arms reach the active screen's `Update`.

## Conversation screen

All paths below are in [`internal/tui`](../internal/tui). The conversation screen is one Bubble
Tea model, `conversationModel`, spread across the files below. `conversation_startup.go`
predates the split and keeps the startup handshake.

| File | Responsibility |
| --- | --- |
| `conversation.go` | Layout and tick constants, `voiceTickMsg`, the `conversationModel` struct, `newConversation`, `activityEntry`, `plainChatTurn` |
| `conversation_update.go` | `Update`, `voiceTickCmd`, `rearm`, `shouldAnimateVoice`, `ensureVoiceTick` (also armed from `startConversation` in `turndriver.go`) |
| `conversation_events.go` | `handleEvent`, the pipeline event switch, with `setStatus` and `setVoiceMode`; it is their main caller, but `setStatus` is also called from `tui_runtime.go`, `tui_screens.go`, `turndriver.go`, `talkhotkey.go` and `work.go`, and `setVoiceMode` from `turndriver.go` |
| `conversation_transcript.go` | Transcript and activity content: append, clear, remember, streaming deltas, refresh, seeding, clipboard copy and the `composerIdle` gate it uses |
| `conversation_scroll.go` | Size, reflow, viewport follow and scroll |
| `conversation_selection.go` | Persona selection request/result messages and the selection queue |
| `conversation_view.go` | `View`, tabs, turn and bubble rendering, metric and activity formatting, animation styles, and `truncateJobLine`, whose only caller is `work.go` |

`handleEvent` is the largest declaration in the conversation model and stays whole; changes to
how an event is handled go there, not into a new file. The activity feed spans two files: its
content lives in `conversation_transcript.go`, its formatting in `conversation_view.go`.

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
