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
Unknown or ambiguous targets, unavailable personas, and classifier failures are
reported without silently choosing another persona.

This private structured request uses the current Ollama model, public names and
aliases, and up to eight recent conversational messages. It has no tools or
conversation-history side effects and a 15-second deadline. It adds a model
request before the reply; its latency depends on the installed model.

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
“Continuing.” followed by a tools-disabled continuation, after an attempted
compact. At most two continuations run. Exhaustion produces “I lost my train of
thought — can you repeat the last part?” and the pipeline marks the turn
degraded. Cancellation stops recovery without another fallback utterance.

| Setting | Default | Effect |
| --- | --- | --- |
| `continuity_projection_enabled` | `true` | Project tool results; disabling restores raw tool history. |
| `continuity_archive_enabled` | `true` | Keep bounded in-memory archive and cap ordinary hot history. |
| `continuity_soft_limit_ratio` | `0.80` | Token-estimate compact threshold; `0` disables this trigger. |
| `continuity_compact_filler` | `true` | Speak before automatic compaction. |
| `continuity_integrity_enabled` | `true` | Detect truncated completions. |
| `continuity_recovery_max` | `2` | Bounded continuation attempts; `0` preserves detection-only behavior. |
| `continuity_num_ctx_ceil` | `8192` | Opt-in adaptive context ceiling, at most `32768`. |

The warm default remains `ollama_num_ctx: 8192`. Expansion follows
8192 → 16384 → 32768 under remaining pressure after older history is archived,
or on the second continuation attempt. The expanded size remains the session
high-water mark and is used by both chat and warmup. Explicit larger user
settings are not reduced. The adaptive ceiling stays **8192 by default** until
live voice, model memory use and latency acceptance are recorded; tests with a
stub server do not establish hardware performance.

Worker integrations can call `AddDelegationNote(brief, jobID)` to use the same
projection and archive boundary. This change does not start or cancel workers.

Validation covers tool-result projection in streaming and full-response loops,
UTF-8 limits, paired session round trips, archive recall after eviction,
compaction filler/failure, continuation success/exhaustion/cancellation, and
sticky chat/warmup options. Microphone/speaker incident replay, live model
follow-up quality, RAM consumption and TTFT benchmarking require separate live
acceptance; they are not implied by these deterministic tests.
