# Capture trace extension and observer contract

Design contract, 2026-10-27. New fields and commands below are **specified implementation**, not existing functionality. [Fixtures](contracts/overlap-interrupted.jsonl) and [dialect](contracts/obey-voice.yaml) are executable design examples.

## Existing format and compatibility

Extend the existing `/record talk` and `/record lab` bundle. Keep `manifest.json` schema `obey-voice.capture/v1`, the JSONL header `v: 2`, record type names, legacy seconds fields and transcript behavior. Add header `trace_schema: "obey-voice.trace/v1"`; this independently versions the additive trace extension. Legacy readers ignore new fields/types. No extension marker means legacy evidence with reduced capability; do not infer precise response or segment identities from event proximity.

Keep `--transcript-log` and its legacy constructor behavior. Add capture-specific options through the existing transcript writer implementation; `/record` uses the richer projection and bounded policy. Do not make work mode or its coordinator a capture dependency. Add compatibility tests for every maintained reader: the duet header check currently only accepts v1, while today's writer already emits v2.

## Common fields

Keep top-level `type`, `ts` and `seq`. `ts` remains RFC3339Nano UTC observation wall time; `seq` is a monotonically increasing capture sequence assigned **before** enqueue/drop decisions. Source order and source time are distinct. Every extension record includes `trace`:

| Field | Type / meaning |
|---|---|
| `capture_id` | Nonempty unique ID for this recording; never reused on restart |
| `runtime_id` | Existing `ConversationScope.RuntimeEpoch`; identifies the source monotonic clock domain |
| `session_id`, `conversation_epoch` | Existing session and conversation identity, copied at source |
| `mono_ms` | Finite number: source observation offset from the runtime's declared monotonic origin |
| `turn_id` | String or null; diagnostic invocation ID allocated before STT, independent of response admission |
| `parent_turn_id` | String or null; causal predecessor for a continuation invocation |
| `turn_start_ms` | Finite number or null; runtime offset of that invocation's existing `turnMetrics.start` |
| `response_id` | Existing admitted `ResponseEnvelope.ResponseID`, or null before admission / for unrelated background work |
| `persona_id` | Frozen response persona, or null when no attributed response exists |

Allocate the diagnostic turn ID at `newTurnConductor`, carry it through text/voice/continuation paths and source context, and retain existing response IDs. Continuations get distinct invocation IDs with an explicit parent reference. Worker/job delivery IDs retain their existing meanings; do not rename them to this diagnostic turn ID.

The pipeline retains a monotonic origin associated with its existing runtime epoch, available even when work mode is off. All producers stamp using that same origin. The recorder must receive immutable context; it must not label a delayed worker/segment event with whichever response is currently speaking. Unscoped events stay on the session lane with null identities. Source correlation additions belong in typed events/source context, not a heuristic in the debugger.

Header extension fields: `clock_origin: "runtime_start"`, `content_policy: "talk" | "lab"`, `cut: "boundary" | "mid_turn"`, and bounded `environment` metadata (engine build revision, selected providers/models, platform/architecture). This is a comparison label, not a dump of config or credentials. Add `capture_id`, `trace_schema` and recording health fields to the manifest.

Session clear/new-session/runtime replacement seals the capture and turns recording off with a notice. It does not cancel the conversation. A new `/record talk` starts a new uniquely named bundle. Persona changes within a conversation keep the capture and preserve per-response identity. Handle reset and capture stop through the existing TUI lifecycle owner; test late events after detach.

## Interval records

Add one typed `PipelineTrace` diagnostic event for low-rate stage boundaries, projected as `type: "trace_span"`. Reuse timestamps already held by `turnMetrics`, `synthWork`, `synthesisObservation` and `playbackEvent`. Emit from the source boundary with immutable identity; do not time stages inside the viewer or infer them from text deltas.

`span` fields:

| Field | Meaning |
|---|---|
| `id` | Stable operation ID, unique within runtime; separate from display label |
| `parent_id` | Parent operation ID or null; optional absent parent remains inspectable |
| `stage`, `lane` | Stage name and display grouping; generic strings interpreted through the dialect |
| `segment_index` | Integer >= 0 or null; assigned when the synth work item is queued and copied through playback |
| `attempt_id` | String or null; primary and fallback provider attempts have different IDs |
| `state` | `start`, `update`, or `end`; update is a cumulative snapshot of this operation's known bounds |
| `start_ms`, `end_ms` | Finite runtime offsets or null, never synthetic zero for a missing measurement |
| `status` | `running`, `completed`, `failed`, `cancelled`, `interrupted`, `timed_out`, `skipped`, or `unknown` |

Known end must be >= known start. If source observations contradict this, retain the raw event with an invalid-timing badge and omit its duration. Cancellation while queued ends the queue span as cancelled and produces no successful synthesis/playback span. Primary failure followed by fallback retains both attempt records; a segment index is not itself an attempt ID.

First-release source stages: turn, STT phase, admission/routing, model request, segment queue, synthesis request/first PCM, playback, tool and worker lifecycle. First PCM, first model chunk and finalized input can be milestone updates with `start_ms == end_ms`; measured zero is valid. Fine-grained device/VAD/provider internals without source observations remain labeled unavailable. Software playback timestamps mean local sink acceptance/Done, not a calibrated acoustic measurement.

New capture subscriptions use `ObserveBeforeFanout` with an early type allowlist and bounded copied payloads. Keep high-rate audio levels/frames outside this path. Never recursively emit bus events or perform file I/O from the observer. Existing non-capture transcript subscribers can retain their legacy ordering.

## Final metrics and reconciliation

Preserve `timing` directly from `events.TurnTiming` and `audible_sink` directly from `TurnMetrics`. Keep the existing `segments` seconds fields and gap partitions. Append response envelope identity, and `trace.turn_start_ms` so segment offsets can be located on the common runtime axis. The model completion offset and local playback completion have different meanings in legacy flat fields; use the normalized contract when available.

Project final segment boundaries into the same `(runtime_id, response_id, segment_index, attempt_id, stage)` operations used live. Explicit live source boundaries are authoritative. Final metrics fill missing boundaries only; a disagreement over 1 ms is shown as conflicting evidence rather than overwritten. Final metrics without a source turn anchor remain a per-turn relative view. Parent and child durations overlap and are never summed as turn latency.

Legacy v2 captures can be inspected and show their stored per-turn segment-relative metrics, but get `Legacy capture: response correlation and timing origin incomplete`. Avoid automatic cross-turn merges or end-to-end latency claims. A fallback's aggregate segment metric cannot reconstruct separate attempts; only explicit attempt observations can do that.

## Writer limits and capture health

These are selected initial limits, to be tested and tuned against the acceptance baseline rather than presented as measured performance:

- 1,024 queued/in-flight records and 8 MiB of copied payload combined; 1 MiB maximum encoded record, matching the current replay line ceiling. Bound strings/collections before copying; oversize emits an explicit omitted-record diagnostic retaining identity and size, not partial JSON. Avoid serializing full arbitrary event structs on the bus.
- Preserve existing preference for terminal/user/metrics records. New span endpoints outrank token deltas. Maintain cumulative lost sequence ranges, coalesced to at most 128 ranges; a coalesced range may overstate loss but must not hide it. All loss/truncation counters remain visible.
- Writer batches up to 64 KiB or 50 ms, whichever comes first; sync at most once per second plus normal seal. Live visibility follows complete newline writes, not fsync. Label persisted/synced progress separately; crash durability can lag live display.
- Limit each capture's event file to 256 MiB, reserving 64 KiB for health and final records. At the limit, disable this capture and report the limit while the conversation continues. No rotation or truncation of an active capture; no automatic deletion of old bundles. Show path and size so the user can remove saved captures deliberately.
- Publish in-memory health to the TUI and a `capture_health` record at most once per second when changed: dropped/skipped totals, bounded missing ranges, write errors, queued bytes, last written and last synced sequence. The health channel must remain available when the disk is broken; it cannot depend solely on writing the damaged file.
- `/record off` detaches immediately, then seals asynchronously. Target drain deadline: 2 seconds. On timeout, show incomplete/unsealed; do not block TUI quit or claim durability. Standard OS file I/O may remain blocked: cap outstanding writer/seal goroutines to one for that capture and do not spawn repeated retries/new writers while it is stuck. Process exit can abandon it; retain a visibly unsealed manifest.
- Normal seal appends `capture_end` with final counts/status, syncs, and updates the manifest atomically. File/manifest permission remains 0600 and directory 0700. Exclusive unique directory creation fixes same-second capture reuse. Header and manifest capture identity must agree when the manifest is present. A copied standalone JSONL remains valid for inspection using its header alone.

`/record talk` retains its existing content behavior; no new capture mode is needed. The trace extension only adds selected timing/config metadata and must not add raw prompts, hidden reasoning, credentials or full config. The viewer inspects the source artifact and should not silently make additional plaintext log copies. Raw audio remains the existing opt-in lab behavior.

## File source and observer UI

Specified new commands:

```sh
agent-stream-dbg observe --dialect obey-voice --follow /path/to/capture/events.jsonl
agent-stream-dbg observe --dialect obey-voice /path/to/capture/events.jsonl
```

The first reads from byte zero and keeps following; the second stops ingest at EOF but leaves inspection open. Open the explicit path; no guessing among processes. Header/manifest session identity is visible. Existing `replay` and `timeline` commands remain supported.

Implement a generic `file` transport using the existing `Transport` frame contract. It knows line framing and follow/EOF, not voice stages. It supports Close cancellation; Send returns a read-only-source error. Its lifetime follows Close, not the initial Connect timeout. Keep ACP's prompt-oriented SessionTransport unchanged. A separate observer model mode attaches in Init without calling setup/send.

- Poll for appended bytes every 50 ms; read complete newline-terminated records and buffer at most 1 MiB of an unfinished line. A follow-mode EOF is idle, not completion. At explicit sealed end, process remaining records then stop ingestion. A missing footer at user stop/non-follow EOF is an unsealed capture, not an invented successful session end.
- Truncation, inode replacement or capture ID change ends ingestion with `Source changed; reopen explicitly`. Do not silently reconnect to a different run. In a saved/unsealed file, show a partial final line as incomplete; while following, wait for its completion. Oversize/malformed complete lines become visible frame errors and resynchronize at the next newline; do not drop silently or kill the entire observer for one bad record.
- Bound file-read channel to 256 frames/4 MiB. Reader backpressure affects only file consumption, never producer recording. Keep 10,000 decoded events/32 MiB and at most 100 expanded turn summaries in UI memory. Eviction is explicit (`Earlier history on disk`); load selected older turns from the source on demand using a sparse offset index (initially one entry/256 records, at most 4,096 entries; double spacing when full). Index work is cancellable. Do not retain unbounded RawSSE/Frames/event duplicates.
- Group records by immutable runtime/turn/response/job identity; unknown scope gets its own session lane. Duplicate `(capture_id, seq)` is idempotent only when content agrees; disagreement is a capture conflict. Gaps and missing span starts stay visible. Use source monotonic times for intervals and receipt time only for ingest-lag diagnostics.
- Observer controls: `[`/`]` select prior/next turn; `Ctrl+F` follows the latest turn; existing pane keys and event details remain. Pausing follow freezes selection/scroll only, while bounded ingestion continues. Replace prompt input with source/health status; disable new-session, send and control actions. Quitting closes only the viewer.
- Timeline shows source start/end bars, per-segment queue/synth/playback overlap, status, and gap partitions. The generic projector consumes dialect-normalized fields (`span_id`, `parent_span_id`, `start_ms`, `end_ms`, `span_state`, `segment_index`, source clock and scope). It must not require private event names such as `flow_step_start` or sum nested stages. Existing interactive dialects retain their views.
- Decode additive unknown fields; unknown types remain inspectable with source timestamp. An unsupported trace major version disables interval interpretation while allowing raw inspection. Check finite numbers and JSON shape before projection.

## Performance gate

Implementation must measure, not assume, the effect of adding tracing. Use the same synthetic event workload and isolated real TUI provider/config/input population with capture off/on; randomize alternating order, separate cold and warm runs, record sample counts and variation. Initial budgets: p95 observer callback <= 250 microseconds at 100 events/s including representative metrics payloads; sustained queue/storage memory within the caps; p95 software first-audio regression <= max(10 ms, 2% of baseline). These are design acceptance budgets, not results. If noise prevents a conclusion, collect sufficient paired evidence or report the gate unresolved. File-to-view freshness target is p95 <= 150 ms on a responsive local disk; it is distinct from voice latency.
