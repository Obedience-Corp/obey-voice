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

## Who does the work

A spawned job runs on one of two backends, chosen per persona by its
`brain.work` setting (`obey-voice persona edit <id> --work own|worker`, or the
**Work** row of the persona form):

- `own`: the persona's own backend and model do it. The job gets a new
  instance of that backend, built from the persona's settings: its provider,
  its model (`claude_model` for a persona on Claude, for example), its prompt
  and its starting directory. It runs in a session of its own. The session
  the persona talks in is not resumed or touched, and none of the job's tool
  calls or output enter it.
- `worker`: the configured worker does it: the persona's `brain.worker`,
  else `voice_worker_provider` and `voice_worker_model`, or a tier.

Left empty, a persona hands its work to the worker when it is on Ollama or
when its profile names a worker of its own (`brain.worker` with a provider,
model or host), which is how every persona worked before the setting existed.
Any other persona on Claude, Codex, Cursor, Grok, Muse Spark or Obey does its
own work. A worker chosen only in Settings (`voice_worker_provider`) does not
change that default. A persona set to `worker` behaves exactly as before.
`obey-voice doctor` lists every persona with who does its work and why.
Choosing, per task, between the two is not offered.

Only who runs the job changes. Whether a turn is a job at all is decided as
before, and the persona still acknowledges it with "I'll work on that and
report back." The result is recorded as that persona's line and spoken in
its voice when the conversation is idle, like any job it started. While it
works you can keep talking to it or to another persona. `/workers` shows the
persona's own provider and model for the job.

An own job runs with the same permissions as a worker on that backend. With
`voice_tools_enabled` on:

| Backend | As its own worker |
|---|---|
| Claude | its own tools, `--permission-mode bypassPermissions`, session persistence off, in the persona's directory, `Bash(camp list)` denied while `voice_worker_boundary` is on |
| Codex | workspace-write sandbox with no network and approvals `never` |
| Cursor | full agent mode with `--trust`; force and yolo stay off |
| Grok | its own tools in bypass-permissions mode |
| Muse Spark | its native tools inside Muse's sandbox, approvals `never` |
| Obey | the daemon's supervised mode |
| Ollama | the persona's model on the persona's Ollama host and context window, with the worker tool set and the fence below |

With `voice_tools_enabled` off, an own job runs with tools off, as a worker
does. An Ollama persona doing its own work uses its own `ollama_num_ctx`
rather than `voice_worker_num_ctx`; when `ollama_num_ctx` is unset, the job
uses the model's advertised window, as a worker would.

The limits are the worker's: `voice_worker_job_budget`, the concurrency limit
(`voice_worker_max_concurrent`, or the tiered policy's), cancelling by voice,
**Stop task** and **Stop all** in `/workers`, and **Allow background workers**
all apply to own jobs. Outside tiered mode an own job's only time limit is
`voice_worker_job_budget`, which is unlimited by default.

In tiered mode an own job is held to the tier its spawn was routed to, with
the persona's `worker_tiers` overrides applied, as a worker job is; only the
backend and model differ:

- The tier's `runtime_seconds` stops it, or `voice_worker_job_budget` when
  that is shorter.
- On Ollama, the tier's `model_requests`, `tool_calls`, `transport_retries`
  and `output_tokens` are counted and enforced, as for a local tier worker.
- On Claude, Codex, Cursor, Grok, Muse Spark and Obey the backend runs its own
  tools and requests, which the host cannot count, so, as for a tier worker on
  those backends, only the runtime limit is enforced.
- Under the `strict` charge policy an own job on Ollama is held to the strict
  tool allowlist. An own job on any other backend is refused with a message
  that says so, since the backend cannot be held to that allowlist, and
  `obey-voice doctor` flags the persona.
- A spawn routed to no tier is refused, as it is for a worker.

The tier's assignment, billing, `enabled` flag and `allow_uncapped` describe
the worker it names and do not apply to an own job. Neither do the paid-worker
switch (`paid_enabled`) and the `worker-usage.sqlite` ledger, which cover paid
tier workers only. An own job reports its usage as any worker does, in
`worker.usage` events, with request and tool counts on Ollama in tiered mode
and whatever token or cost figures the backend returns.

The experimental `voice_work_enabled` inbox runs local workers only, so there
a persona on a hosted backend hands its work to the worker as before, and
only a persona on Ollama can do its own.

A long story ("tell me a long story") is written by the persona's own backend
when it does its own work, with tools as `voice_tools_enabled` says, so it
needs no configured worker. In tiered mode, and for a persona that hands its
work to the worker, the configured worker writes it, as before. The story
prompt carries none of the conversation, so it gives no hosted notice.

The job's brief is the one every job gets (see
[Hosted backends](personas.md#hosted-backends)): a persona on Claude, Codex,
Muse Spark or Obey sends its own backend only its share of the conversation.
When the job is the first thing sent to a hosted backend in the conversation,
the hosted notice is shown before it starts. A change to `brain.work` reaches
a conversation the next time that persona is selected: a new conversation, or
when it is addressed again after another persona answered. It does not
restart the persona's session. Who does a turn's work is decided once, when
the turn is taken, so what the router is told and who runs the job agree even
if the persona is edited or switched while the turn runs.

## What the worker is doing

After a conversational request is admitted as background work, the originating
persona acknowledges it without waiting for the worker result. If the task is
still waiting for a slot, the acknowledgment says it is queued. The same reply
is retained in the conversation for muted and text-only sessions. Rejected
admission does not produce an accepted-work promise. This acknowledgment is
independent of the optional progress narration below and makes no additional
model request.

A spawned worker's tool calls reach the bus like the live brain's, one
`tool_call_started` / `tool_call_finished` pair each. Both carry the call `id`,
the tool `name`, a coarse `kind` (`read`, `list_dir`, `write`, `execute`,
`web_search`, `web_fetch`, `skill`, `task`, `route`, `other`), and the finish
carries the result preview, the leading `error: …` line when the tool failed,
and how long it took.

A `job_id` on those events attributes the call to a background job; empty means
the live conversation. Job-attributed tools update the job's activity and
transcript card, while the footer shows compact worker counts. They never
touch the live turn's status line or orb, so a job running behind a
turn cannot hijack the conversation. `job_queued`, `job_started`, `job_finished`,
`job_failed` and `job_aborted` bookend the work: queued when the table accepts
the job, started when it actually begins (a spawn behind a running job waits
for that job's terminal event), and the last three carry elapsed time. The
footer reads `Workers: 1 running · 2 queued · /workers`.

"Cancel", "cancel that", "kill it" or "kill that" ends the work: every running
and queued job is aborted, and she says so once for the lot — "Stopped after
40 seconds.", or just "Stopped." inside the first second — adding "Dropped 1
queued job." when something never got to start. "Stop" on its own only
silences speech; the job keeps running.

Those events are folded for display: consecutive calls of one kind become a
row, a kind change opens the next. The TUI keeps one transcript entry per job
and rewrites its summary in place — `⚙ job-1 · read 3 files · ran 1 command`,
sealed with `· done in 35s`, `· failed: …` or `· killed after 6m5s`. Below that
summary, individual tools show their commands or targets, observed status,
output previews and errors. These details remain after completion. The `/workers`
manager shows each task's provider/model, elapsed time, activity and output.
Without the TUI each row prints once as `[job-1]
read 3 files`, when its kind gives way or the job ends. "What's happening"
answers `Still working on {task}. So far: read 3 files · ran 1 command.` The
spoken `{task}` is the request's first clause, at most ten words; the manager
and transcript keep the whole request. With a persona on a hosted backend
answering, the answer names only the jobs that persona started, on any backend,
and counts the rest (see [Hosted backends](personas.md#hosted-backends)).

In the TUI, `/workers` provides **Stop task** (`s`), **Stop all** (`x`, then
`y`), and **Allow background workers** (`t`). The permission is global, not
persona-specific, and is saved as `voice_workers_disabled` (default `false`).
Turning workers off rejects new spawned and managed jobs but lets already
admitted running and queued work finish. Stop all cancels those existing jobs
without changing admission permission. A managed cancellation is a request;
the detail view reports the runtime's termination evidence, including
`unknown` when a stop cannot prove execution ended. No worker control requires
a spoken request or a model turn. These controls are independent of the
experimental `voice_work_enabled` result inbox.

`voice_work_narration` decides whether a job talks while it works: `status_only`
(default) stays quiet until asked; `milestones` speaks one short line ("Read 3
files.") each time a row closes, through the same idle gate as a job summary.
Never one line per call.

In legacy mode, two singleton limits bound spawned work.
`voice_worker_max_concurrent` (default `1`) is how many jobs run at once; the
rest wait in order, and "what's happening" names the running job first, then
counts the others ("Also 1 more running and 1 queued.").
`voice_worker_job_budget` (default `0`, no limit) optionally caps how long any
one job may run. With no limit a job runs until it finishes or you cancel it,
and with one job at a time a stuck job holds the queue until then. When a
limit is set and a job passes it, the job is stopped, `job_aborted` carries
`reason: budget`, and she says so once — "I stopped {task} after 10 minutes.
It ran past its budget." A job you cancel yourself carries `reason: cancelled`; the cancel
line above is spoken once for the batch, never once per job.

Both stops are the job table's own act, not a request to the worker: at the
budget, or on a cancel, the job is marked aborted, its slot goes to the next
queued job, and the terminal event fires at once. The worker is told to stop
through its context — Ollama requests, shell tools and CLI-backed providers all
receive the cancel — and a worker that ignores it lingers on its own goroutine
without counting against the limit; whatever it returns later is dropped.

Each job is a conversation of its own. The worker starts with no history and
no CLI session from the job before it, and two jobs running at once never
write into the same one; the brief carries whatever context the job needs.
A Claude worker also runs with session persistence off, so its transcripts
never land in `~/.claude/projects` beside your own sessions for that
directory. Grok has no such switch: a Grok worker's sessions still accumulate
under `~/.grok/sessions` for the working directory, one per job.

The JSONL transcript records `id`, `kind`, `job_id` and `elapsed_s` on
`tool_call` / `tool_result`, plus one `job` record per transition carrying
`state` and `elapsed_s`.

## Where a worker may go

A worker stays under the directory it was launched in. When that directory
sits inside a campaign, the boundary widens to the campaign root; when it
does not, the working directory itself is the boundary, and nothing about
campaigns or festival is needed (`voice_worker_boundary`, default on). Two
layers hold it:

- **The prompt says so.** Every worker, whichever brain runs it, gets a
  `Boundary:` block after the environment block: work only under this
  directory, do not open other folders under home or any tool's session
  store, never `git commit` or `git push` outside it. Only a campaign root
  adds the line about other campaigns and `camp list`.
- **The Ollama worker's tools refuse.** `read_file`, `write_file` and
  `list_files` refuse any path outside the boundary, judged the way the
  kernel would open it: each symlink followed where it stands, dangling or
  not, `..` applied after the link rather than to the text, and a file that
  does not exist yet judged by where it would land. `run_command` refuses a
  command that names a path under your home directory but outside the
  boundary (by the same rule), a relative path that climbs or links out, or
  `camp list`; the rest of the machine (`/usr`, `/opt`, `/tmp`) stays open.
  A refusal comes back to the model as the tool's error and reaches the bus
  as `tool_call_finished` with `err`, so the attempt is visible in the
  activity feed and the transcript. The shell check reads the command's
  words; it is a fence, not a sandbox.

A Claude worker also has `Bash(camp list)` denied through the CLI, a rule
that is inert where `camp` is not installed. Grok and Claude run their own
file tools, so for them the boundary is the prompt; the Ollama worker, the
local-first product, has both layers.

## Remaining WI-ebe14b scope

This implements managed worker admission, asynchronous acknowledgment, compact
briefs, host-serialized unsolicited delivery, status, explicit cancellation,
timeouts, and session cleanup. The legacy path waits for an existing listen boundary. The opt-in canonical
inbox uses the capture-owner handoff described below; it does not cancel capture
and assume the microphone is free.

Remote A2A targets, worker continuation/input-required UX, and recovery of voice
tickets after process restart remain outside this first managed integration.
Obey's durable results remain available independently of this process. Automated microphone/provider validation is still required for the acoustic acceptance
gate; use explicitly recorded physical microphone and speaker selections in a
fresh isolated process and retain device identities, negotiated rates, measured
signal correlation and transcription. Unit tests exercise scripted capture/playback
and the real CLI process boundary, not an actual paid worker.

The original seed's one-tool restriction is revised: explicit cancellation and
cached status need model-callable controls. All three names already belong to
Obey's vocabulary; no blocking wait is exposed.


## Tiered worker limits and accounting

Workers Settings can save separate Simple, Medium and Complex assignments.
Queued jobs retain the assignment and limits they were admitted with. Tiered
mode uses the shared concurrency and queue limits in `voice_workers.policy`
(defaults: one running, 32 queued) and each tier's runtime limit (120 seconds by
default), instead of the legacy singleton limits above. Switching to tiered
mode does not silently migrate or reinterpret the legacy singleton keys; use
the explicit migration preview in Settings if you want to copy the current
worker into all three tiers.

Local Ollama workers enforce request, tool, retry and optional output-token
allowances across the job. Missing generated-token usage blocks another
output-bounded request. Strict policy excludes shell, network and delegation
tools whose billable effects cannot be bounded. Runtime and concurrency limits
cover local scheduling; cancellation does not prove remote or descendant exit.

Paid work defaults off. Current Claude and Grok CLI adapters have no certified
hard charge cap. To use one, explicitly choose its billing label and
`account_ref=default`, enable paid workers, select advisory global policy, and
allow uncapped use for that tier. Strict global policy blocks these adapters
even when a tier allows uncapped use. Their request, tool and output ceilings
remain unavailable; a saved number does not enforce an opaque CLI's internals.
Legacy paid assignments do not bypass the conversation inbox's policy.

The installation's `worker-usage.sqlite` is local accounting, not a billing API.
All personas and sessions share it. A durable reservation and dispatch marker
precede paid execution. CLI-reported costs are observations and cannot settle
an unknown charge. Cancellation, timeout and uncertain provider termination
retain the record. Worker result preparation belongs to this scope; the host
voice-model turn and TTS have their own provider billing.

Choose **Worker allowance** in Settings to inspect known reserved and settled
micro-USD, unresolved unknown bounds and any strict-dispatch freeze. Advisory
mode does not promise a hard remaining allowance. Unresolved reservations carry
across midnight; changing timezone takes effect after the active period.
Storage failures block paid admission. No current adapter has authoritative
reconciliation or a supported user-adjustment command; unknown records remain
unresolved. The JSONL transcript records `worker.usage`, `worker.limit_reached`
and `worker.charge`; unknown counts or costs are explicit null values.

## Inspecting and delivering retained results

With `voice_work_enabled: true`, `/work` opens the current conversation's work
summary. `/work JOB_ID` shows the frozen tier/provider, originating persona,
observed state and delivery disposition. `/work logs` shows saved diagnostic
locations. The selected responder can change while a job keeps its original
assignment. Naming Santa selects Santa for subsequent conversation; the job's
origin does not require repeating a persona name for each reply.

The work strip stays visible while work is active or an update remains
uncommunicated. Reading a card clears its local unseen indicator; it does not
consume the pending update. A held result names its reason, such as muted or
unavailable audio. Use these commands with a retained job or result ID:

| Command | Effect |
| --- | --- |
| `/work cancel JOB_ID` | Requests cancellation of execution. |
| `/work cancel all` | Requests cancellation of current workers. |
| `/work dismiss JOB_ID` | Dismisses a retained result; does not cancel its worker. |
| `/work speak JOB_ID` | Makes a held result eligible for the host's next safe delivery boundary. Output must be available. |
| `/work replay JOB_ID` | Explicitly delivers a previously communicated retained result again, without rerunning tools. |

`/workers` retains the worker manager, admission switch and stop controls.
Opening either inspection view preserves the conversation draft. Escape leaves
selected work details, then returns from Activity to Chat. Copying chat includes
readable outcomes once, without progress chatter or hidden reasoning. Saved-file
inspection and export are described in [Background-work diagnostics](work-diagnostics.md).

## Experimental rollout and rollback

The conversation inbox remains opt-in (`voice_work_enabled` defaults to `false`)
while automated acoustic acceptance is pending. Deterministic tests and a recorded-audio
run do not establish physical microphone behavior or interruption latency; the gate requires
measured speaker-to-microphone signal correlation, known-phrase transcription and 30
comparable baseline/worker turns using the same recorded device configuration.
Tests may explicitly select working system audio devices without changing OS
defaults. Automated signal, transcript and timing assertions are the acceptance
method; human listening is not required. Results apply only to the tested devices,
audio settings and provider configuration, not every microphone or speaker setup.
The capture owner now supports a frame-boundary completion handoff; hosts whose
adapter cannot acknowledge it defer delivery explicitly. The legacy path's
listen-timeout boundary is not the scheduler guarantee for the new inbox.

Simple, Medium and Complex are effort labels, not fixed model names or automatic
price levels. Configure each assignment in Workers Settings. No missing tier
silently selects a paid model. Changing an assignment applies to new jobs;
queued jobs retain their plan, while the current paid-disable switch is checked
again before dispatch. Migration previews the effective legacy assignment,
including persona overrides, and saves the tier document atomically. Returning
to legacy mode preserves the tier document and original singleton settings.

For a code rollback, explicitly select legacy mode before using an older build.
Keep the complete session files, `<session-id>.work` directories and
`worker-usage.sqlite` together in a backup. Current work-disabled binding and
session saving preserve sidecars and pending canonical data. An older binary's
reader may ignore newer fields and its writer may omit them on save; the
compatibility tests do not certify every historical binary. Diagnostic sidecars
are never restore inputs and must not be used to rerun jobs. Restored outcomes
remain held until an explicit resume or delivery action. No migration promises
exactly-once physical hearing across a crash between playback and saving its
receipt.
