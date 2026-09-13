# Background-work diagnostics

Background-work diagnostics are retained evidence for inspection. They do not
replace the canonical session state. `session.Work` in the normal session store
is the authority for current jobs, outcomes, delivery, and restoration. The
transcript/session v2 format is unchanged. A diagnostic journal may be absent,
partial, or stale without making its records authoritative.

## Storage layout and schema

A session's diagnostic sidecar is a directory named `<session-id>.work`. It
contains runtime-specific event segments and checkpoints:

```text
<session-id>.work/
  events-<runtime-id>-000001.jsonl
  snapshot-<runtime-id>.json
  details/<opaque-reference>.txt
```

Records and checkpoints use schema `obey-voice.work/v1`. Sequence numbers are
monotonic within a runtime; separate runtime epochs never share a sequence.
Checkpoint files are diagnostic projections and are never restore inputs.
Files and directories are created owner-only. Detail references are opaque and
validated against traversal and symlink paths.

The journal records normalized, already-redacted diagnostics. A worker detail
payload is stored separately under a stable hash reference and the event keeps
bounded metadata (`original_bytes`, retained bytes, truncation, or an
unavailable reason). Normal records contain no raw audio, hidden reasoning, or
full tool output. Provider identity in records is the configured/observed
adapter identity; the journal does not establish billing or provider truth.

## Bounds and durability

Default bounds are:

- 4,096 queued records and 8 MiB queued/in-flight encoded data;
- 8 MiB event segments and a 64 MiB per-session event retention target;
- 256 MiB for global sealed-session retention;
- 64 KiB per detail and 16 MiB of details per session;
- 30-day sealed diagnostic expiry;
- a 250 ms sync interval for the journal dispatcher.

The journal dispatcher owns one bounded queue and accounts for records in
flight as well as waiting records. Queue overflow drops diagnostics, preserves
the latest checkpoint in memory, and records explicit health/gap evidence. A
disk or checkpoint failure is reported through health independently of the
worker result and does not prevent canonical work from remaining available.

Event segments are synced before a checkpoint is atomically replaced. The
checkpoint uses a temporary file, file sync, same-filesystem rename, and
parent-directory sync. Persisted sequence and timestamp advance only after
those operations succeed. Retention removes event/detail evidence while
preserving diagnostic checkpoints and canonical session files; retention
notices carry the removed sequence ranges.

## Reading incomplete history

The reader is read-only and persisted-only. It never executes journal data or
claims live coordinator state. It deduplicates identical `(runtime, sequence)`
replays and reports conflicting duplicates. It detects missing initial or
intermediate sequences, checkpoint-reported gaps, retention markers, malformed
records, unsupported schemas, and unsealed runtime checkpoints.

An unterminated final JSONL line is treated as an uncommitted crash tail and is
ignored while retaining the committed prefix. A malformed complete line keeps
the useful prefix but returns an error and marks the report incomplete. An
unsealed checkpoint means the persisted view may be live or uncleanly ended;
it does not prove that the runtime exited. A missing sidecar means diagnostic
history is unavailable, while the saved canonical session state may still be
current and complete.

Sealing is runtime-specific. A transient retired/cleared work snapshot is not
itself a durable seal because the session may reuse that runtime identity.
Global sealed cleanup prunes only checkpoints explicitly marked sealed; active,
unsealed, unknown-schema, and caller-selected sidecars are preserved.

## Inspect, follow and export

```sh
obey-voice sessions inspect SESSION_ID --work --json
obey-voice sessions events SESSION_ID --job JOB_ID --json
obey-voice sessions events SESSION_ID --job JOB_ID --follow --json
obey-voice sessions events SESSION_ID --detail OPAQUE_REFERENCE
obey-voice sessions export SESSION_ID --diagnostics --output ./work-diagnostics.json
```

These commands load saved files without running the interactive startup hook,
resuming work, changing the selected responder, or producing speech. Inspection
shows source paths, capture time, saved age and canonical persisted revision.
The selected responder and each result's frozen originating persona are separate
fields. Diagnostic checkpoints carry their own persisted sequence and time.

Read a job's `work.snapshot` records to see its admission, state and result;
work-enabled execution does not emit every legacy `job.*` event. Tool records
show the observed outcome and available exit or HTTP status. `work.delivery`
records describe projection and audio attempts. A projected result or an audio
attempt alone is not proof of successful delivery; canonical communicated state
and its delivery receipt supply that evidence. Missing evidence remains unknown.

Default exports omit separate detail bodies, transcript turns and non-allowlisted
configuration. `--include-details` adds only retained, redacted tool previews,
up to 64 KiB each and 16 MiB total. These previews may be shorter than the original
output. Expired or missing detail references produce an explicit limitation.
Redaction covers known configured credentials and recognizable credential
patterns; arbitrary sensitive prose cannot be identified reliably.

Export creates an owner-only local file and refuses to replace an existing file
unless `--overwrite` is explicit. The selected source session and its diagnostic
directory cannot be export destinations. Inspection and exports are bounded
projections of saved state, not backups of the canonical session.

Command exit statuses are: 0 for success, 1 for operational failure, 2 for invalid
usage, 3 for a missing session, 4 for an unsupported schema, and 5 for incomplete
diagnostic history. Useful partial inspection/export output can accompany a
nonzero status. Following emits JSONL records and explicit notices and stops on
cancellation. It observes committed newline-terminated records; a crash tail is
not promoted to an event.

Retention preserves the active segment and other unsealed runtime evidence.
Consequently, a session can temporarily exceed its retention target when no
older segment is eligible for removal. Global cleanup applies to sealed event
and detail evidence, not canonical session files or retained checkpoints.

## Conversation work controls

`/work` opens work status in the existing Activity pane. `/work JOB_ID` opens
that job's results and tool evidence; `/work all` clears the selection. Escape
clears the detail first, then returns to Chat. The composer draft is preserved.
`/workers` remains the execution manager, including its background-work toggle
and confirmed Stop all action.

The reserved Work strip stays above the composer while execution or delivery is
outstanding. `updates` counts uncommunicated results and `new` counts result
cards not yet visible in Chat or explicitly opened. Seeing a card does not
fulfil speech delivery. Narrow layouts abbreviate running, queued, updates and
new as `r`, `q`, `u`, `n`. A held update stays visible after it has been seen.
Below 20 columns or 6 rows the terminal asks to resize.

- `/work cancel JOB_ID` requests execution cancellation; termination may remain
  unknown. `/work cancel all` applies it to current workers.
- `/work dismiss JOB_ID` removes the pending delivery obligation. It does not
  cancel execution. A completed, already communicated result has no pending
  obligation to dismiss.
- `/work speak JOB_ID` resumes a held result, or requests replay of a retained
  communicated result. `/work replay JOB_ID` explicitly requests the same
  delivery action. Audio must be available and enabled. Neither reruns the
  worker. Captured input and the foreground turn retain priority.
- `/work logs` shows local paths and CLI inspection examples.

Explicit replay preserves the immutable outcome and result ID, creates a new
pending delivery attempt, and requires a fresh matching receipt before it is
communicated again. Restoring a session holds an unfinished replay; merely
opening the session or result does not request another announcement.

`/copy` includes the latest readable result when it follows the last assistant
reply. `/copy all` includes each retained result once with its task and source;
progress rewrites and hidden reasoning are excluded.
