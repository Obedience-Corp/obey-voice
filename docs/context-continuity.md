# Ollama conversation continuity

Tool-heavy Ollama turns now project results before they enter conversation
history. A deterministic JSON projection prioritizes identifiers, titles and
status fields; each note is at most 2048 UTF-8 bytes. Non-JSON results use a
bounded excerpt. Native tool roles and call IDs remain paired on the wire and in
session files, so a projected result does not become an assistant instruction.

The original result is retained only in a session-local memory archive, capped
at 32 KiB per entry and 50 entries. Evicted conversation blocks are archived too.
When the archive fills, the two oldest summaries are folded into one bounded,
lossy note and their raw payloads are discarded. Ordinary hot history is trimmed
at 12 messages to at most 10, subject to preserving an active user/tool group.
Explicit references to `WI-…` or `job-…`, or requests containing “earlier”,
“previous”, or “what did we”, can recall matching summaries as paired tool data.
Compaction also consumes recent archived notes when they fit the request budget.
This is bounded recall, not semantic search or durable memory. Clearing the
conversation clears the archive. The shared canonical transcript is persisted
separately from this bounded working context.

## Worker generation limits

When an Ollama worker receives `done_reason="length"`, the full-response loop
continues the same task with the partial answer and existing tool results. It
allows at most two continuation requests across the whole worker run, including
all tool rounds. Completed tool exchanges stay in context and are not replayed
by the harness. Calls from a truncated response are withheld entirely; the model
must reissue them in full before they can execute. Host continuation instructions
and partial drafts are private to the request; only the completed answer enters
conversation history.

Each continuation uses the same worker request, output-token, runtime and
admission budgets. A continuation is a new generation, so it does not spend the
transport-retry allowance. Unknown token usage under a capped output budget,
cancellation, insufficient context space, or exhausted job/continuation budgets
still stop the task with an error. Recovery attempts are logged. This recovery
does not restart foreground responses stopped by `ollama_response_tokens`,
group replies, result notifications, or internal compaction requests.

The private instructions are editable through the normal prompt resolver at
`system/worker-continuation.yaml` and `system/worker-truncated-tools.yaml` under
`prompts_dir`. They have no runtime placeholders; malformed edits fall back to
the packaged YAML with a diagnostic.

## Persona threads and shared conversation

The session owns one complete shared transcript, with the original persona and
response metadata on each reply. Each selected persona gets its own Ollama
conversation projection: its own replies are native assistant messages, while
other personas' contributions are attributed shared context. Switching transfers the current working context, including an existing
summary and newer messages, and reprojects ownership for the receiving persona.
The full transcript remains available for persistence; a switch does not replay
compacted-away history or request a fresh summary. Compaction still runs when
the receiving model's context budget or configured message-count limit requires
it.

In a group chat, the system prompt identifies the current persona and available
agents. Other speakers use compact labels such as `Santa agent:`, `Lance user:`,
and `Unknown user:`. The human speaker-name table supplies user names; persona
aliases are not human identities. Own prior replies keep their native assistant
role.

Response IDs stay in application metadata. They are not inserted into model
conversation text. Resuming and compacting use the same ownership rules; a
continuity summary is shared context, not a new statement by the selected
persona. Existing saved transcript content is retained for review.

Older replies without persona metadata remain shared context when a named
persona is selected. Their authorship is unknown, so they are not assigned to
that persona. Legacy tool exchanges remain readable context, with their
original call pairing preserved in the saved transcript.

To exercise an installed local model through the real selector, pipeline, and
session persistence without microphone capture or playback:

```sh
OBEY_VOICE_LIVE_PERSONA=1 go test -tags=integration ./internal/persona/runtime \
  -run TestLivePersonaThreadHandoff -count=1 -v -timeout 5m
```

The default test model is `qwen3.8:latest`; set
`OBEY_VOICE_LIVE_PERSONA_MODEL` to use another installed model. The test fixes
sampling at temperature 0 and seed 42. It covers switching, restored legacy
markers, and compaction; normal model responses remain generative.

## Addressing and handing over the floor

The Ollama persona runtime classifies the finalized utterance before selecting
its responder. Names may occur anywhere: both “Santa, what do you think?” and
“What do you think, Santa?” can select Santa. The classifier uses the sentence
and recent conversation, not an invocation phrase list. Ordinary mentions,
quotations, and questions about a persona leave the current responder selected.
The user's original words remain unchanged in the transcript and model input.
Messages with no roster name and no acoustic name cue stay with the current
responder without a classifier call. A leading punctuated address gives that
listener the first turn, including requests to consult another persona.
Semantic routing selects only configured agent names and aliases. Human names,
model names such as LFM3, and other unlisted topics remain ordinary conversation
for the selected responder. The structured output schema constrains target names
to that roster; the host also ignores unlisted semantic targets if a backend
ignores the schema. Unknown exact `@id` and `switch to Name` controls, ambiguous
agent targets, unavailable configured personas, and classifier failures still
report an error without silently selecting a replacement.

This private structured request uses the current Ollama model, public names and
aliases, and up to eight recent conversational messages. It has no tools or
conversation-history side effects. Its deadline is set by
`persona_routing_timeout` (default 120 seconds), including model startup. Parent
cancellation still stops the request. It adds a model request before the reply;
its latency depends on the installed model.

That classifier exists only when the live brain is Ollama. When optional
decisions and `decision_addressee` are on and the runtime is ready, the decision
model answers the same question first (same inputs, same roster restriction and
validation), so natural addressing and agent handoffs also work while a persona
on Claude, Codex or another backend is live. A harness backend is never asked to
classify. If the decision model fails, abstains or is not ready, an Ollama live
brain classifies as above; any other brain falls back to the name grammar and
otherwise keeps the current persona, as it did before the decision model existed.
`persona_routing_timeout` bounds both models together. Accuracy of the decision
model on real speech has not been measured. See
[Who is being addressed](decision-routing.md#who-is-being-addressed).

After a successfully completed response containing a persona name, the same
classifier can recognize an
agent's deliberate question or invitation to another agent. The receiving agent
answers the shared thread without a fabricated user message. These follow-up
responses have tools disabled. Each user turn permits at most three additional
responses and no repeat responder, so agents cannot sustain a conversation loop.
Background notifications, failed or interrupted replies, and local greeting
acknowledgements do not initiate handoffs. User interruption retains priority.

Voice responses prepare the next listening buffer before classifying a handoff.
They retain up to `vad_pre_roll_ms` of recent processed microphone audio and keep
new samples arriving during classification. This prevents the next listen from
erasing the start of an utterance. Detected barge-in retains the in-progress
utterance; explicit close/cancel clears the retired listening scope. Both offline
and streaming STT require admitted speech evidence, so a noise-only VAD segment
cannot become a transcript through an RMS-only fallback.

An opt-in check for the local model's addressing decisions:

```sh
OBEY_VOICE_LIVE_ADDRESS_MODEL='<installed-model>' go test -tags=integration \
  ./pkg/voiceagent/brain -run TestLiveGroupAddressee -count=1 -v
```

## Stopped replies

A reply you stop is kept as you were given it, marked unfinished: text the
model had already sent but the conversation had not yet shown or spoken is
left out, and a stopped reply of which you were given nothing is dropped. A
reply still being delivered when you stopped it is marked unfinished even if
the model had finished generating it. The model's next request, and any
persona that answers next, receive the reply as it is kept. It is recorded
before the stopped turn ends, unless the request takes more than five seconds
to stop, so a save straight after it keeps it; the next line, a persona
switch or an idle job delivery waits until it is recorded. When
the model chose to hand the request to a worker and the turn stopped before
it did, your line stays in the conversation.

## Repeated answers

Live Ollama replies check for a narrow replay pattern: the new answer starts
with the entire last completed answer from the same persona, at least 200
characters long. A matching prefix is held before synthesis; a fresh response
streams as soon as it diverges. Matching ignores whitespace but does not detect
paraphrases or repeated passages later in an otherwise new response.

Explicit repeat cues, requests of three words or fewer, and requests too long
for the intent check preserve the original response. Otherwise a private,
tools-free check of the latest message uses the selected model with a 30-second
deadline. Only an `unrequested` decision triggers one tools-free regeneration.
Uncertain or failed checks preserve the original output; a failed check emits
an informational diagnostic. The classifier remains model-dependent, so this
is a conservative mitigation rather than a guarantee against all repetition.

Rejected attempts never enter speech, displayed replies, or canonical history.
A second repeated attempt produces a short recovery message and a degraded
outcome. Cancellation stops recovery without speaking another line. Background
notifications, workers, internal summaries, and incomplete-answer continuations
do not use this guard. Matching replies can take longer to reach first audio
because they wait for the generation and intent check before being released.

```sh
OBEY_VOICE_LIVE_REPETITION_MODEL='<installed-model>' go test -tags=integration \
  ./pkg/voiceagent/brain -run TestLiveRepetitionIntent -count=1 -v
```

## Context budgets

`read_skill` is special: its full instructions remain in the existing transient
skill splice for the active turn, while its persisted tool result is projected.
An instruction set that cannot fit the safe request budget causes an explicit
recovery response instead of silently omitting the tail of the skill.

Before a new turn, the pipeline checks estimated prompt size including incoming
text, in addition to its existing turn-count trigger. When compacting, voice
output says “Give me a moment to gather my notes.” once before the summary
request. A failed compact stops that incoming request and reports a degraded
response. Model requests have a final check including tool schemas, tool-call
arguments, archive recall and continuation instructions; the estimate remains
approximately four bytes per token and is not an exact model tokenizer.

A truncated completion is marked incomplete in history. The same stream carries
a tools-disabled continuation after an attempted compact, without a spoken
recovery announcement. Trailing voice stage directions do not make a complete
sentence count as truncated. At most two continuations run. Exhaustion produces “I lost my train of
thought — can you repeat the last part?” and the pipeline marks the turn
degraded. Cancellation stops recovery without another fallback utterance.

| Setting | Default | Effect |
| --- | --- | --- |
| `continuity_projection_enabled` | `true` | Project tool results; disabling restores raw tool history. |
| `continuity_archive_enabled` | `true` | Keep bounded in-memory archive and cap ordinary hot history. |
| `continuity_soft_limit_ratio` | `0.80` | Token-estimate compact threshold; `0` disables this trigger. |
| `continuity_compact_filler` | `false` | Speak before automatic compaction. |
| `continuity_integrity_enabled` | `true` | Detect truncated completions. |
| `continuity_recovery_max` | `2` | Bounded continuation attempts; `0` preserves detection-only behavior. |
| `continuity_num_ctx_ceil` | `32768` | Opt-in adaptive context ceiling, at most `32768`. |

The conversation window and adaptive ceiling default to `32768`. Existing
explicit settings are preserved. With an 8k floor and a larger configured
ceiling, expansion follows 8192 → 16384 → 32768 under remaining pressure after
older history is archived, or on the second continuation attempt. The expanded
size remains the session high-water mark for chat and warmup. These limits do
not imply a measured latency or hardware-memory result.

Worker integrations can call `AddDelegationNote(brief, jobID)` to use the same
projection and archive boundary. This change does not start or cancel workers.

Validation covers tool-result projection in streaming and full-response loops,
UTF-8 limits, paired session round trips, archive recall after eviction,
compaction filler/failure, continuation success/exhaustion/cancellation, and
sticky chat/warmup options. Microphone/speaker incident replay, live model
follow-up quality, RAM consumption and TTFT benchmarking require separate live
acceptance; they are not implied by these deterministic tests.

## Conversation memory and compaction display

Ollama conversations default to a 32k window with a matching 32k ceiling and
`max_history: 40` as its history setting. Workers use `voice_worker_num_ctx`:
the default `0` resolves the worker model's advertised window, with a 128k fallback
when metadata is absent. Positive values select an independent worker window,
including sizes above 32k. Workers compact between tool rounds at
`continuity_soft_limit_ratio` (80% by default), preserving the original task,
summarized progress and completed tool findings without replaying commands. Raw
tool evidence remains in the canonical transcript. Summary requests use the same
worker model and count toward its request/output/runtime budgets; incomplete
summaries fail without replacing the working context.

Existing explicit window settings are preserved, including saved `8192` values
from older installs. Use `obey-voice config unset voice_worker_num_ctx` to return
to automatic mode, or set a positive token count to choose your own window.
The next conversation uses the new setting. Settings → Brain →
Conversation memory offers 8k, 16k and 32k presets; selecting one writes the
conversation window and ceiling together in one atomic config update. It takes
effect in the next conversation.

The composer status row shows estimated context usage, effective window and
percentage, including session-only expansion. Usage updates during long streamed
replies and after each response, including full-response requests. The same row
shows trim, summarize and reseed progress. A `~` marks approximate counts; the
meter performs no model requests. Before the first usage event it says `waiting`
without drawing an empty bar. Providers without usage telemetry show
`Context unavailable`. Usage display remains enabled when optional truncation
detection is disabled. Narrow terminals shorten the meter; below 32 columns it
is hidden to preserve the input status.
Compaction is silent by default. `continuity_compact_filler: true` retains the
optional spoken notice.

Automatic compaction budgets its summary instruction before requesting it. If
summarization fails, complete old exchanges are archived and removed from the
working projection until the next prompt is below the soft limit. The saved
canonical transcript is preserved. The user turn then proceeds normally;
compaction cannot keep retrying against the same oversized working history.
Canceling the turn cancels compaction and clears its progress display. Explicit
`/compact` still reports a failed summary instead of silently claiming success.
