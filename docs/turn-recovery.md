# Turn recovery and token usage

[Back to README](../README.md)

Runtime errors are text-only. Chat shows an **Error** notice with the failing
stage and the actual error message, and Activity keeps the same details. The
notice stays in the conversation after the status changes. Desktop notices can
be selected and copied.

Failed turns preserve any reply already generated, but do not add an apology,
synthesize an error announcement, or save a fabricated assistant answer. A turn
can still be marked `completed (degraded)` so the conversation remains usable.
Narration failures and automatic worker failure/budget notifications are also
text-only; asking for job status still produces a normal spoken response.

Typing to stop speech cancels synthesis and queued audio for that turn while
allowing its reply text to finish. This is an interrupted output, including when
typing happens before the first audio; it does not produce playback cancellation
errors. The next turn can speak normally. Actual playback failures remain errors
and mark the turn degraded.

An Ollama response stuck repeating a syllable inside an exceptionally long prose
word is stopped before it can flood chat and speech. Normal deltas, code blocks
and explicit literal-repetition requests are preserved. A repetition or output
limit stops queued speech immediately and reports the failure as text, without
automatically restarting the generation. Already displayed partial output remains
in the transcript and is marked incomplete for future model context.

For Ollama, Activity records per-request `prefill N tok · gen M tok`. Prefill
tracks the size of your new turn, not the whole transcript, and is bounded by
`ollama_num_ctx` (default `32768`). Workers have a separate window:
`voice_worker_num_ctx` defaults to `0`, which resolves the selected worker model's
advertised capacity (128k if metadata is absent). An explicit positive setting
overrides that capacity. Workers compact automatically between tool rounds at the
continuity soft limit, retaining the task and tool findings. Startup warmup sends that same `num_ctx` —
omitting it lets Ollama allocate the model's full window (262k on qwen3/kimi)
and a serve restart loop will pin a runner per spawn. `ollama_num_batch`
(default `128`) decides how much of the prompt is re-read each turn. Ollama
otherwise derives it mainly from the context window — 512 at or below 4096, 1024
above, 2048 above 32768, downgraded toward 512 if the model's predicted VRAM
will not fit — and llama.cpp keeps its last reusable checkpoint about one batch back
from the end of the prompt, so a large batch means a conversational turn reuses
almost nothing and re-reads the persona and history from scratch. On
`qwen3.8-uncensored:27b` that cost 4.3 s before every spoken word; `128` skips
89 % of the prompt instead of 14 % and cut the median first token to 864 ms
(`docs/benchmarks/B2-qwen-ttft-mechanism-2026-08-28.md`). Brand-new prompts read
slightly slower, so this trades the tail for the median. `0` restores Ollama's
own choice. `ollama_keep_alive`
(default `10m`) keeps the
model resident between turns; `ollama_think` (default `auto`) turns thinking
on for qwen3-family models so chain-of-thought is not spoken. Explicit `false`
on qwen3 dumps untagged CoT into Content, which can be spoken.

See [Configuration](configuration.md) for the matching keys.

## Speech stopped before the reply finished

Press **Ctrl+T** in the active conversation to inspect Activity. An
`interrupted: barge_in` entry means the microphone interruption detector stopped
playback; it does not prove that a person spoke. Playback leaking back into the
microphone can produce the same detector decision. Other stop/error entries help
distinguish cancellation, playback failure and a stalled turn.

The timing line keeps `interrupted`, `failed`, or `timed out` visible after
listening resumes. An unsuccessful local playback is labeled `playback stopped`,
not `playback done`. The displayed assistant text may be complete even when its
audio was interrupted.

The TUI writes low-rate lifecycle traces to its existing log, independently of
experimental work journaling:

```sh
tail -f ~/.obey/agents/voice/obey-voice/logs/native-diagnostics.log
```

Custom config roots and debug-audio runs place the log under their own directory.
`turn_trace` records capture/transcription phases, model/playback milestones,
response/session identifiers, interruption reason and terminal outcome.
`barge_in_decision` records the qualifying neural speech count, audibility,
startup state and speaker-gate decision. With `barge_in_speakers=enrolled_only`,
automatic interruption and pause-audio retention require a healthy, fresh enrolled
match. Disabled, unavailable, unknown or stale recognition cannot admit playback
as a new user turn through these paths. Enable live speakers and enroll your voice
to use this policy; typed/manual interruption remains available. `anyone` retains
its permissive behavior. Older logs showing `stale_fail_open` identify the previous
fallback that allowed interruptions without a fresh speaker match. These records
contain no audio, transcript, model output or tool output.
They describe the detector's evidence, not a verified acoustic source.

Older binaries do not write these trace records; installing an update cannot
reconstruct an earlier interruption. For a suspected feedback loop, headphones
can isolate speaker playback from the microphone while investigating.
