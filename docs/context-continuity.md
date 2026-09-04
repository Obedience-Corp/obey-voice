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
conversation clears the archive; session files contain only hot history.

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
