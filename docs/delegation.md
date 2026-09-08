# Background tasks through Obey

Opt-in background tasks let the Ollama voice conductor queue a managed worker,
acknowledge the request immediately, and keep answering unrelated questions.
A finished task's compact brief is scheduled through the host's ordinary turn queue. Obey owns
execution, policy, budgets, durable results, and cancellation.

## Required integration

This consumes the managed delegation **CLI schema version 1**, including
`obey delegation capabilities`, `start`, `await`, and `cancel`, from [Obey PR #358](https://github.com/Obedience-Corp/obey/pull/358). OA0002 alone is insufficient: it shipped
managed execution internally, but its external start/status RPCs did not expose
result text or cancellation. A successful OA0002 festival does not establish
that this voice client can use the complete lifecycle.

The voice build also needs [WI-b94dad / PR #108](https://github.com/Obedience-Corp/obey-voice/pull/108) and its `AddDelegationNote` projection boundary.
Enabling the setting without either dependency fails startup with a concrete
error. The client never exposes partially usable worker tools. No public Go
imports of Obey's internal protobuf package or separate voice A2A stack are used.

## Configure

Use an existing active Obey parent session with controlled managed delegation,
an allowed managed-child profile, and the appropriate campaign grants. The
parent must remain available for the voice session. Set these in the voice
config file or with `obey-voice config set`:

```yaml
brain_provider: ollama
voice_tools_enabled: true
delegation_enabled: true
delegation_binary: obey
delegation_socket: "" # empty uses Obey's configured local Unix socket
delegation_campaign_id: YOUR_EXISTING_CAMPAIGN_ID
delegation_parent_session_id: YOUR_EXISTING_PARENT_SESSION_ID
delegation_profile: YOUR_APPROVED_PROFILE
delegation_timeout: 300
```

Restart the voice session after changing these settings. They belong to the
host, not the persona. Delegation is off by default. Set `delegation_enabled`
back to false to disable it. The normal per-turn tools gate still applies,
including the remote session's tools setting.

Ask for a self-contained task such as “Check the active campaign workitems in
the background and tell me which one is ready.” The model can use:

- `agent_delegate`: queue one task; the acknowledgment contains a local ticket
  and explicitly says that Obey admission is pending.
- `agent_delegations`: read cached status immediately, without waiting.
- `agent_cancel`: request cancellation for the exact ticket or durable ID, only
  on explicit user intent.

The model is not given `agent_await`, remote target selection, profile selection,
or campaign/parent identity arguments. The task plus the appended spoken-answer
instruction must fit Obey's 2048-byte context limit. One pending task per voice
session bounds both background work and delivery memory.

## Delivery and shutdown

The worker is asked for two or three short prose sentences. Non-prose or long
results go through a stateless summarization pass; a failed or malformed summary
produces a short explanatory message instead of raw JSON, markdown, or code.
Only this brief is projected into conversation history, with the durable Obey
ID as its source reference. Raw results remain in Obey.

Serve and the TUI text mode announce results when idle. Active user turns and
already-queued requests finish first. In the local voice TUI, an active STT
listen retains ownership until the current utterance finishes or its configured
no-speech timeout expires; a result notification never cancels a possibly
starting utterance. The background reply uses normal progressive speech and VAD
barge-in. No synthetic user utterance appears in the transcript.

Ready briefs join the current user turn's context group, so context-budget
trimming cannot evict them as an older exchange. They are acknowledged only
after a successful final model request that included the note. For unsolicited
background replies, acknowledgment waits until playback completes; acoustic
barge-in keeps both the result pending and the user's captured audio for the next
turn. A failed or interrupted model turn keeps the brief pending for retry. Compaction/meta turns do
not consume results. Delivery means the conductor received the brief; the
model chooses how to include it in its spoken reply.

Clearing or restoring a voice session requests cancellation and immediately
switches to a fresh runtime; old ready notes and late results cannot enter the
new conversation. Same-session compaction preserves workers. Persona replacement
retires the previous brain in the background. Final shutdown joins current and
retired cleanup so admission can resolve before its child is canceled.

Interrupting speech does not cancel background work. Explicit cancellation,
the configured task timeout (measured from queueing), and voice-session cleanup
request cancellation through Obey. A cancellation request is not presented as
proof that the worker stopped. Admission runs outside the turn context and
retries with the same idempotency key when its outcome is uncertain. Shutdown
may wait for admission to resolve so a newly created child can be canceled;
three admission attempts are bounded at 45 seconds each, followed by a bounded
cancel request. Unconfirmed admission retains its ticket and blocks further
work until the operator checks Obey and restarts the voice session.

## What the worker is doing

A spawned worker's tool calls reach the bus like the live brain's, one
`tool_call_started` / `tool_call_finished` pair each. Both carry the call `id`,
the tool `name`, a coarse `kind` (`read`, `list_dir`, `write`, `execute`,
`web_search`, `web_fetch`, `skill`, `task`, `route`, `other`), and the finish
carries the result preview, the leading `error: …` line when the tool failed,
and how long it took.

A `job_id` on those events attributes the call to a background job; empty means
the live conversation. The distinction is load-bearing: job-attributed tools
update the `Working: {task} · {tool}` chip and the activity feed only, and never
touch the status line, the orb, or the transcript, so a job running behind a
turn cannot hijack the conversation. `job_queued`, `job_started`, `job_finished`,
`job_failed` and `job_aborted` bookend the work: queued when the table accepts
the job, started when it actually begins (a spawn behind a running job waits
for that job's terminal event), and the last three carry elapsed time. The chip
names the running job and counts the ones running alongside or queued behind
it.

"Cancel", "cancel that", "kill it" or "kill that" ends the work: every running
and queued job is aborted, and she says so once for the lot — "Stopped after
40 seconds.", or just "Stopped." inside the first second — adding "Dropped 1
queued job." when something never got to start. "Stop" on its own only
silences speech; the job keeps running.

Those events are folded for display: consecutive calls of one kind become a
row, a kind change opens the next. The TUI keeps one thread line per job and
rewrites it in place — `⚙ job-1 · read 3 files · ran 1 command`, sealed with
`· done in 35s`, `· failed: …` or `· killed after 6m5s` — and the chip reads
`Working: {task} · {rows}`. Without the TUI each row prints once as `[job-1]
read 3 files`, when its kind gives way or the job ends. "What's happening"
answers `Still working on {task}. So far: read 3 files · ran 1 command.` The
spoken `{task}` is the request's first clause, at most ten words; the chip and
the transcript keep the whole request.

`voice_work_narration` decides whether a job talks while it works: `status_only`
(default) stays quiet until asked; `milestones` speaks one short line ("Read 3
files.") each time a row closes, through the same idle gate as a job summary.
Never one line per call.

Two limits bound spawned work. `voice_worker_max_concurrent` (default `1`) is
how many jobs run at once; the rest wait in order, and "what's happening"
names the running job first, then counts the others ("Also 1 more running and
1 queued."). `voice_worker_job_budget` (default `600` seconds) is the longest
any one job may run: past it the job is stopped, `job_aborted` carries
`reason: budget`, and she says so once — "I stopped {task} after 10 minutes. It
ran past its budget." A job you cancel yourself carries `reason: cancelled`;
the cancel line above is spoken once for the batch, never once per job.

Both stops are the job table's own act, not a request to the worker: at the
budget, or on a cancel, the job is marked aborted, its slot goes to the next
queued job, and the terminal event fires at once. The worker is told to stop
through its context — Ollama requests, shell tools and CLI-backed providers all
receive the cancel — and a worker that ignores it lingers on its own goroutine
without counting against the limit; whatever it returns later is dropped.

The JSONL transcript records `id`, `kind`, `job_id` and `elapsed_s` on
`tool_call` / `tool_result`, plus one `job` record per transition carrying
`state` and `elapsed_s`.

## Remaining WI-ebe14b scope

This implements managed worker admission, asynchronous acknowledgment, compact
briefs, host-serialized unsolicited delivery, status, explicit cancellation,
timeouts, and session cleanup. Immediate preemption of an idle local STT listen
is deferred: without an atomic capture handoff it can discard speech just
starting, so the existing listen timeout defines that turn boundary.

Remote A2A targets, worker continuation/input-required UX, and recovery of voice
tickets after process restart remain outside this first managed integration.
Obey's durable results remain available independently of this process. Live
microphone/provider validation is still required for the hardware acceptance
gate; unit tests exercise scripted capture/playback and the real CLI process
boundary, not an actual paid worker.

The original seed's one-tool restriction is revised: explicit cancellation and
cached status need model-callable controls. All three names already belong to
Obey's vocabulary; no blocking wait is exposed.
