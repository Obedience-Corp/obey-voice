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
including the remote session's tools setting. With `voice_router_tools:
json_route`, a turn that offers these tools still routes with the `route` tool,
because the JSON route applies only when `route` is the only tool.

Ask for a self-contained task such as “Check the active campaign workitems in
the background and tell me which one is ready.” The model can use:

- `agent_delegate`: queue one task; the acknowledgment contains a local ticket
  and explicitly says that Obey admission is pending.
- `agent_delegations`: read cached status immediately, without waiting.
- `agent_cancel`: request cancellation for the exact ticket or durable ID, only
  on explicit user intent. The host checks the request first: the tool can
  cancel only the task its own session started. Any other ticket, another
  persona's managed task or a background job's id is refused as an unknown
  background task.

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
change that default. Because such an install had its jobs on that worker
before, the first time a persona on a hosted backend does its own job there,
an info line says that its jobs now run on its own backend and that
`obey-voice persona edit <id> --work worker` hands them back. It is shown once
per persona, not spoken and not sent to any model, and recorded in
`notices.json` next to `config.yaml` so it is not shown again after a
restart; a persona whose `brain.work` is set never shows it. A persona set to
`worker` behaves exactly as before. `obey-voice doctor` lists every persona
with who does its work, why, and how long its own jobs may run.
Choosing, per task, between the two is not offered.

Only who runs the job changes. Whether a turn is a job at all is decided as
before, and the persona still acknowledges it with "I'll work on that and
report back." The result is recorded as that persona's line and spoken in
its voice when the conversation is idle, like any job it started. While it
works you can keep talking to it or to another persona. `/workers` shows the
persona's own provider and model for the job.

A reply an Ollama persona is giving directly, without handing it off, also
goes on as a job of that persona when you type a message to another persona
while it runs. It is not an own job: it continues the same request in the
persona's own session, with the tools that turn had, its result is delivered
and recorded as described above, it is held only to the live reply's idle
limit rather than `voice_worker_job_budget`, and it starts at once rather
than waiting for a free slot. Work it hands off when it ends does follow the
rules below. See
[Talking to another persona while one is working](personas.md#talking-to-another-persona-while-one-is-working).

An own job runs with the same permissions as a worker on that backend, and
follows `voice_tools_enabled` in every mode, as a legacy worker does:

| Backend | `voice_tools_enabled` on | `voice_tools_enabled` off |
|---|---|---|
| Claude | its own tools, `--permission-mode bypassPermissions`, session persistence off, in the persona's directory, `Bash(camp list)` denied while `voice_worker_boundary` is on | `--tools ""` (no built-in tools) in the default permission mode; persistence and the deny list as with tools on |
| Codex | a new thread in a workspace-write sandbox with no network and approvals `never` | a read-only sandbox with shell, exec, patch, apps, plugins, MCP servers and web search off |
| Cursor | full agent mode with `--trust`; force and yolo stay off | `--mode ask`, Cursor's read-only ask mode rather than no tools |
| Grok | its own tools in bypass-permissions mode, in the persona's directory | `--tools ""` |
| Muse Spark | its native tools inside Muse's sandbox, approvals `never`, no session log | the empty preset with write, shell and web tools disabled |
| Obey | the daemon's supervised mode | refused under the default `obey_tool_policy: strict`, since Voice cannot enforce tools off; with `daemon`, the daemon's discussion mode, whose tool permissions the daemon decides (see [Obey daemon](obey-daemon.md)) |
| Ollama | the persona's model on the persona's Ollama host and context window, with the worker tool set and the fence below | no tools |

Each job starts a new session or thread and never resumes the one the persona
talks in. Claude's job session is not saved; a Codex job thread is not marked
ephemeral, as for a Codex worker.
An Ollama persona doing its own work uses its own `ollama_num_ctx`
rather than `voice_worker_num_ctx`; when `ollama_num_ctx` is unset, the job
uses the model's advertised window, as a worker would.

The limits are the worker's: `voice_worker_job_budget`, the concurrency limit
(`voice_worker_max_concurrent`, or the tiered policy's), cancelling by voice,
**Stop task** and **Stop all** in `/workers`, and **Allow background workers**
all apply to own jobs.

An own job on a hosted backend (Claude, Codex, Cursor, Grok, Muse Spark,
Obey) is stopped after 15 minutes while `voice_worker_job_budget` is `0`, its
default, in legacy and tiered mode alike. A budget you set wins, longer or
shorter. `voice_worker_job_budget: -1` removes this limit and the worker's
on purpose; in tiered mode the routed tier's `runtime_seconds` still applies.
In legacy mode `obey-voice doctor` warns about each persona that then does
its own hosted work with no limit. An own job on Ollama keeps the worker's
limit, which `0` leaves unset.

Decision: the paid-worker switch (`paid_enabled`) is not applied to own jobs.
Choosing a persona on a hosted backend is the consent to that backend's use,
and the hosted notice tells you before any of the conversation is sent to
it; the 15-minute default bounds a job you did not otherwise limit.

In tiered mode an own job is held to the tier its spawn was routed to, with
the persona's `worker_tiers` overrides applied, as a worker job is; only the
backend and model differ:

- The tier's `runtime_seconds` stops it, or the own-job time limit above when
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
- A spawn routed to no tier, or to a disabled one, is refused, as it is for a
  worker. With every tier disabled, own work is refused and the router is
  not told work is available.

The tier's assignment, billing and `allow_uncapped` describe the worker it
names and are not checked when an own job is admitted. Neither are the
paid-worker switch and the `worker-usage.sqlite` ledger, which cover paid
tier workers only. What the router is told about which tiers are available
still comes from the worker's tiers, so an enabled tier whose worker is held
back by the paid-worker switch or its billing settings is described to the
router as unavailable even for a persona doing its own work.

An own job reports its usage as any worker does, in `worker.usage` events,
with request and tool counts on Ollama in tiered mode and whatever token or
cost figures the backend returns.

The experimental `voice_work_enabled` inbox runs local workers only, so there
a persona on a hosted backend hands its work to the worker as before, and
only a persona on Ollama can do its own.

A long story ("tell me a long story") is written by the persona's own backend
when it does its own work, as an own job: with tools as `voice_tools_enabled`
says, the hosted notice first on a hosted backend, the own-job time limit, the
strict policy and the routed tier in tiered mode, and the persona and backend
recorded on the job in `/workers`. It needs no configured worker. In tiered
mode the "tell me a long story" phrase names no tier, so such a story is
refused, as a spawn with no tier is; a story the model routes to a tier is
written under that tier. A persona that hands its work to the worker has the
configured worker write it, as before; tiered mode builds no such worker, so
there story writing fails with "worker provider is not configured", as it did
before own work existed.

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

In legacy mode a finished job's answer is spoken through the idle gate, after
any reply already being spoken or waiting its turn. When it will be spoken,
`job_finished` marks it speech-pending and `job_result_delivered` follows once
its audio starts: the chat shows the answer and the conversation history takes
it at that point, so the thread reads in the order it was heard. If that speech
is muted, fails or is dropped, `job_result_delivered` still arrives, unspoken.
With speech off the answer appears with `job_finished`.

"Cancel", "cancel that", "kill it" or "kill that" ends the work: every running
and queued job is aborted as one act, so no queued job
starts on a slot another one freed, and she says so once for the lot. Before
she speaks she waits up to a second and a half for the cancelled workers to
return. When they have, the line is "Stopped after 40 seconds.", or just
"Stopped." inside the first second, adding "Dropped 1 queued job." when
something never got to start. When a worker has not returned yet, the line
says so: "Cancelled after 40 seconds; the worker is still stopping." With
several jobs it says how many are still stopping. The wait is skipped when
nothing was running, and it only chooses the words: the job is already over
and its slot already free. The worker's later return is shown on the job's row
and in `/workers`, not spoken. With the experimental `voice_work_enabled`
inbox the line is "Cancellation requested. Completed results remain in the work
inbox.", with "; the worker is still stopping" after the first sentence when a
worker has not returned. "Stop" on its own only silences speech; the job keeps
running.

The same phrases reach a task an Obey-managed delegation is running, and that
task alone is enough for them to work. Obey is asked to cancel it and she says
"Asked Obey to cancel the delegated task."; Obey reports when it has ended.
The status phrases still answer for jobs only.

What a cancelled job had done is saved with her cancel line in the conversation
history as a host note (`host_note` on the saved turn): `[job-1 (audit the
config) was cancelled before it finished. Done so far: read 3 files, ran 41
commands.]`, from the same activity rows the job's line shows. The note is not
part of what she said: it is never spoken or shown, in the live chat or in a
reopened session. A persona on Ollama or Codex reads it after her cancel line
and can answer from it when asked later what that job got through; other
providers do not read it yet. It is never saved as the job's answer.

A running job can be corrected from the keyboard with `/work tell JOB_ID
TEXT`: "use the other file" reaches that job instead of becoming chat or a
second job. The rest of the line is the correction, as typed. The job keeps its
id, its place and its limits; a correction adds no time, requests or spend. On
the in-process Ollama worker serving a model on this machine (a legacy worker,
a tiered local Ollama worker, or an Ollama persona's own job) the worker reads
it before its next model request or tool call. A request already built is
rebuilt with the correction if it has not been sent yet, including a resend
after a brief outage. A tool call the worker has not started is not run and is
answered `not run: a correction arrived`; a call already running finishes
first. The correction is then the next user turn in the worker's conversation,
after the results, and the worker continues from it. An answer written before
the correction arrived, or while its pictures were being attached, is kept
aside: the worker's saved conversation marks it replaced, and the job goes on
from the correction. If no request is left for that, or the corrected run
fails, including when its pictures cannot be attached, the kept answer is
delivered cut short instead, so a correction never turns an answer into a
failure. Several corrections apply in the order given.
A job still queued gets the correction in the brief it starts with, labelled
`Correction from the user:`. A correction is refused, with the reason, for a
CLI or hosted worker, a paid worker, an Ollama worker on another machine or
behind a cloud alias, an Obey-managed delegation and a reply moved to the
background; nothing about the job changes. When whether the worker runs on
this machine cannot be checked, the refusal says so. Such a worker still reads
a correction given while the job is queued, in its brief. A correction reaches
a job only from the conversation that started it: after a clear, the old
job's id is unknown to `/work tell`, though `/work cancel` still reaches it. Corrections come
from the keyboard, and from a persona's `job_control` tool when that is turned
on (see [A persona controlling its own jobs](#a-persona-controlling-its-own-jobs));
no spoken phrase makes one.

`/work tell` reports at once whether the correction was accepted, added to a
queued job's brief, or refused. What happens to it afterwards is shown on the
job's row and in `/workers`, for the newest correction: `correction included`
once a request carrying it was answered by the model server (which does not
mean the worker obeyed it), `correction added to brief` for a worker that took
it in its brief and cannot report more, `correction could not apply: limit
reached` when a time or request limit left no request for it, in which case
the answer the worker already had is delivered cut short with that limit as
the reason, `correction superseded by cancel` when a cancel came first, and
`correction could not apply: the job failed` or `: the job was stopped` when
the job ended otherwise. When the corrected run fails, the answer delivered in
its place is cut short with the reason "the corrected run failed". A correction
to a job that has already answered is refused as already finished. Each change
is a `job_correction` event with the job id, revision, text and state, and the
session transcript records the revision and whether it went into a brief. A
correction lifts a stop by the repeat guard, so the redirected job gets its
tools back, but never a time or request limit. A job that takes a correction
is never given an image repair: if its task asked for a picture and its answer
has none, the answer is delivered ending `(No image attached.)`, and its row
says `no image attached`. A job that took no correction is repaired as before,
and while that repair runs a correction is refused: "the job is finishing". A
correction that arrives just as the repair would start is taken instead of the
repair. When a corrected run answers nothing, the answer the worker already
had is delivered cut short, as when the run fails. When the worker's context fills, the task and
every later instruction are kept as written and the tool exchanges between
them are summarized, in notes that say how many of those instructions they
come after. A worker keeps its last model request for its answer: with one
left it does not summarize, and with two left a summary the server is too busy
to answer is not resent.

A running job can also be asked for its report with `/work report JOB_ID`:
stop using tools and hand over what was done, instead of cancelling and
getting nothing back. No model decides whether it is accepted. The command
prints at once what the job had done when the report was accepted, from the
same activity rows its line shows (`read 3 files · ran 1 command`, or "no tool
activity recorded yet"), and for an accepted report that checkpoint stays on
the task in `/workers` whatever happens to the job afterwards. On the in-process Ollama worker serving a model on this
machine (the workers a correction reaches) the job keeps its slot and stays
`running`; its row and `/workers` show `reporting`. A tool call it has not
started is not run and is answered `not run: a report was requested`; a call
already running finishes first, including one the worker had committed to run
when the report was accepted even if its start is shown a moment after the
receipt, and a request already sent is answered first,
though one that brings back nothing goes on to the report round. Its next
model request is a tools-free answer round asking it to say what is done, what
is not, and where anything it made is: no summary of its context or skill
check is asked for first, a summary already under way that fails is set aside,
and nothing gives it its tools back, a resend after
an outage or a continuation included. If its conversation is too large to
answer without a summary, the job fails at the context limit as any job
does. There is no
clock on that answer: a slow model takes the time it needs, and a cancel still
ends the job at once, as for any job, leaving the checkpoint as what was
reported. The answer is delivered cut short with the reason "report
requested", as for a limit: `job_finished` carries `cut_short`, the inbox
result is `partial` with "cut short: report requested", and the conversation
saves it as an unfinished reply. A job asked for a picture may report in
words: it is not given an image repair and fails for no missing picture, while
a picture it did attach is still checked and delivered. If the round brings
no answer, the job fails as any forced answer does, unless a correction's run
was under way with an answer already written and held: then that held answer
is delivered cut short as "the corrected run failed". If the job's own final
answer was already written when the report came, that answer is delivered as
usual, not cut short, and `/workers` says the job finished before it was
asked. A job already answering because a limit or the repeat guard stopped it
keeps that reason, and so does a report accepted while that round is being
asked for; a report accepted while a round of tools runs is the reason even if
that round trips the repeat guard. A second report while the job runs changes
nothing and returns the same checkpoint; once the job has ended, a report says
it has already finished. Corrections still waiting when the report is accepted could not
apply ("a report was requested"); one a request already carries is left to be
included; a later `/work tell` is refused with "the job is reporting". A report
and an image repair exclude each other: a job being repaired answers a report
with "the job is already finishing its answer", and a reporting job is not
repaired. A queued job is not reported: it has not started, stays queued, and
`/work cancel` drops it. A job that has started but whose worker is still
getting ready says it is starting and there is nothing to report yet. A CLI, hosted or paid worker, an Ollama worker on
another machine or behind a cloud alias, an Obey-managed delegation and a
reply moved to the background give the checkpoint only and say this worker
cannot be asked to stop, so the work continues; nothing about the job changes,
and the checkpoint is not kept in `/workers`. A moved reply gives it only to
the conversation it came from. An accepted report, and a checkpoint-only one
from a CLI, hosted, paid or remote worker, is a `job_report` event with the job id, the checkpoint and whether a worker
answer will follow, scoped to the conversation that admitted the job like
`job_correction`; an Obey-managed delegation and a moved reply give their
checkpoint in the receipt only. How the job ends is told by its usual terminal
event. A
report comes from the keyboard, and from a persona's `job_control` tool when
that is turned on; no spoken phrase asks for one.

Those events are folded for display: consecutive calls of one kind become a
row, a kind change opens the next. The TUI keeps one transcript entry per job
and rewrites its summary in place — `⚙ job-1 · read 3 files · ran 1 command`,
sealed with `· done in 35s`, `· failed: …` or `· killed after 6m5s`. A cancelled
job whose worker has not returned yet shows `· killed after 6m5s · stopping…`
until it does. Below that
summary, individual tools show their commands or targets, observed status,
output previews and errors. These details remain after completion. The `/workers`
manager shows each task's provider/model, elapsed time, activity and output,
and `same output for N rounds` while a running job's tools keep returning the
same output (see below).
Without the TUI each row prints once as `[job-1]
read 3 files`, when its kind gives way or the job ends. "What's happening"
answers `Still working on {task}. So far: read 3 files · ran 1 command.` The
spoken `{task}` is the request's first clause, at most ten words; the manager
and transcript keep the whole request. With a persona on a hosted backend
answering, the answer names only the jobs that persona started, on any backend,
and counts the rest (see [Hosted backends](personas.md#hosted-backends)).

A persona on Ollama is also told about unfinished jobs whenever it answers you,
so a question that is not one of the status phrases ("what have you been doing
all this time?") gets a true answer. Obey Voice writes the reply that starts a
job, not the persona's model, and a long job outlives the six to twelve turns
the model is sent, so the conversation itself stops showing that the job exists.
Each request that answers a person carries a short list beside your message:
every running or queued job with its state, how long it has run, the request it
was started for (up to 160 bytes), its three newest activity rows, whether
the answering persona or another one started it, and, while its tools keep
returning the same output, for how many rounds. Up to eight jobs are listed,
the answering persona's own first, and any others are counted. The list is
taken once, when the turn begins, and is never saved to the conversation. Result notifications, peer replies and group
discussions do not carry it. The sentence that introduces the list is the
`system/active-work.yaml` prompt. A persona on a hosted backend is not sent the
list; ask it "what's happening" or open `/workers`.

### A persona controlling its own jobs

With `voice_job_control: true`, or `job_control: true` in a persona's `brain:`
block, a persona on Ollama can act on one of its own listed jobs when you ask:
"cancel the summary", "that's enough, what have you got" (a report), "use the
other file instead" (a correction). It is off by default. Small local models
have been measured misusing it: one turned a bare "Stop." into a cancel every
time, a smaller one cancelled another persona's job and started second jobs,
and another said "Done, cancelled" without calling anything. Try it with your
model before relying on it (see [Measuring a model](#measuring-a-model)).

The tool, `job_control(action, job_id, instruction)`, is offered only on a
live turn that answers you, on the direct router: never to a worker, a result
notification, a continuation, a group discussion, a summary, a retried input or
a recovery round, and never under `route_only`, `json_route`, `single_call` or
tiered routing. It sits between `view_image` and `start_background_work` in
the tool list whether or not jobs exist, and the warm-up request carries it
too. With the option off nothing a persona is sent changes, and a
`job_control` call is an unknown tool. Under the route policies a stray call
is never turned into a spawn, with the option on or off.

The model only asks; Obey Voice decides. When a model response holds a
`job_control` call, nothing in that response runs: not the control, not a
search, write, command, picture or hand-off beside it, and it never becomes
background work. The turn ends at the call: the model gets no tool result and
no second request, and text it sends after the call is dropped. What it said
before the call was spoken as usual and stays in its reply. The host then
checks, in order, and stops at the first failure:

1. What you said is not a bare "stop" ("Stop.", "stop!"), read as the cancel
   phrases are read. A bare stop silences speech and never changes a job,
   whatever the model called: nothing is spoken, and the chat shows `A bare
   "stop" only silences speech; say "cancel" to end a job.` "Stop the
   download" is not a bare stop.
2. The turn is still the live one: not interrupted or stopped, not moved to the
   background, the conversation not cleared and the persona not switched since
   it began. Otherwise nothing is spoken and the chat says the control arrived
   after its turn stopped. Each turn's permission is used once.
3. The response was complete, offered the tool, and holds one control: two
   identical calls count as one, two different ones are refused.
4. The job id is exactly one the turn's job list named: the list the persona was
   sent when the turn began, at most eight jobs. No other id, no close match.
5. The job is the persona's own, by who started it, in this conversation. A job
   whose starter is not on record is not its own.
6. The action is one that job can take, by the same rules as `/work cancel`,
   `/work report` and `/work tell`. A clear's leftover jobs, which `/work
   cancel` still reaches, are not reachable from the tool.

Then the control is applied once, and Obey Voice says one line of its own in
the persona's voice, saved as the persona's reply after anything it said
first:

| Outcome | Line |
|---|---|
| cancel, worker returned | `Stopped.` / `Stopped after 40 seconds.` |
| cancel, worker still stopping (after the same short wait as "cancel") | `Cancelled after 40 seconds; the worker is still stopping.` |
| cancel, queued job | `Dropped 1 queued job.` |
| report accepted | `I asked it to report, and it's writing up what it has; so far: read 3 files, ran 1 command.` |
| report, no activity yet | `I asked it to report, and it's writing up what it has; it has recorded no tool activity yet.` |
| report, worker that cannot be asked to stop | `So far: read 3 files; it can't be asked to stop, so the work continues.` |
| report, queued / starting / finishing | `That job hasn't started yet, so there's nothing to report.` / `It's just starting, so there's nothing to report yet.` / `It's already finishing its answer.` |
| correction accepted | `Sent; it reads it at its next step.` |
| correction to a queued job | `Added to its brief; it reads it when it starts.` |
| correction refused | `I couldn't pass that on: ` and the refusal `/work tell` gives |
| job ended since the list | `That job had already finished.` |
| another persona's job | `I can only do that for my own jobs.` |
| unknown, unlisted or missing id, or two different controls | `Which job do you mean? Mine is …` naming only the persona's own listed jobs |
| none of its jobs running | `I have no job running.` |
| incomplete or malformed call, or a steer without an instruction | `I couldn't do that.` |

The report line says the persona asked, since a persona may ask for a report
you did not. A cancel's host note is saved as for a spoken "cancel". The
controls are the ones `/work cancel`, `/work report` and `/work tell` make,
with the same events and `/workers` rows. The exact cancel phrases keep their
own path and never reach the model.

#### Measuring a model

`TestJobControlCorpus` runs a fixed corpus through full spoken turns of the
real feature against a local Ollama model: the conversational provider on the
direct router with job control on, a real pipeline, agency and job table, and
fixture jobs with no model behind them, owned by the speaking persona or by
another one as each case says, with the running times the job list gives
them. Speech goes to a spy and is never played. No tool the persona calls
runs: a search, a command or a write is answered `recorded` and written to the
results, and the requests the model is sent are the ones a real turn sends. A
job it starts is another fixture job. It is skipped unless asked for:

```sh
OBEY_VOICE_JOB_CONTROL_CORPUS=1 \
OBEY_VOICE_JOB_CONTROL_MODEL=<model> \
OBEY_VOICE_JOB_CONTROL_OUT=/tmp/job-control.jsonl \
go test ./pkg/voiceagent/pipeline/ -run TestJobControlCorpus -count=1 -timeout 0 -v
```

`OLLAMA_HOST` picks the server, `OBEY_VOICE_JOB_CONTROL_REPS` the repetitions
(3 by default) and `OBEY_VOICE_JOB_CONTROL_CASES` a comma-separated subset of
case ids. Each repetition is one JSON line: the model's calls, the host's
decision and refusal, the receipt, what actually changed in the job table,
the tools answered without running, every line spoken in order, jobs started
and timings. A rerun resumes past the
lines already written. `<out>.summary.json` scores every line against five
criteria, where a change is a real change to a job, not a call: no wrong job
changed, no unrequested cancel or correction, no call with an empty list, no
job started for a control request, and the right change on at least two thirds
of clear requests.

In the TUI, `/workers` provides **Stop task** (`s`), **Correct task** (`e`,
which returns to the conversation with `/work tell JOB_ID ` typed for you, or,
when you had started a message there, keeps it and shows the command),
**Report task** (`r`, which asks the selected task for its report as
`/work report` does and shows the checkpoint), **Stop all** (`x`, then `y`), and **Allow background workers** (`t`). The permission is global, not
persona-specific, and is saved as `voice_workers_disabled` (default `false`).
Turning workers off rejects new spawned and managed jobs but lets already
admitted running and queued work finish. Stop all cancels those existing jobs
without changing admission permission. Neither stop waits for a worker: a
cancelled job whose worker has not returned shows as `stopping`, with
termination `unknown`, and turns to `aborted` with termination
`confirmed (worker returned)` once it does. A job cancelled before it started
reads `confirmed (never started)`. A managed cancellation is a request; the
detail view reports the runtime's termination evidence, including `unknown`
when a stop cannot prove execution ended. No worker control requires a spoken
request or a model turn. These controls are independent of the
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
one job may run; a job a persona on a hosted backend does itself has a
15-minute default instead (see [Who does the work](#who-does-the-work)), and
`-1` lifts both. With no limit a job runs until it finishes or you cancel it,
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
without counting against the limit; whatever it returns later is dropped. The
table watches for that return: until it comes the job is stopping, and
`job_aborted` carries `Stopping: true`; when it comes, `job_exited` follows.
"Returned" means the job's run function came back, which for a CLI worker
means its process was reaped; it does not prove that processes the CLI started
have ended.

An Ollama worker is also made to stop using tools and answer with what it has
in three cases: it repeated the same tool call with the same result three
times with nothing new in between; a time limit is set and nearly used (a
tenth of it, between 15 seconds and a minute and never more than half, is kept
for the answer); or, in tiered mode, it is down to its last model request. Its
answer is delivered as usual, but the job counts as cut short: the job row says
why ("done in 41s · cut short: repeated the same tool call", "cut short: time
limit nearly reached" or "cut short: request limit reached"), `job_finished`
carries `cut_short` and `cut_short_reason`, and the conversation saves the
answer as an unfinished reply with that reason, so the persona later knows the
job did not finish and why. In the `voice_work_enabled` inbox the result is
`partial` with the limitation "cut short: …". If the worker writes no answer in
that round, the job fails with the reason, as before.

That guard compares each call together with its result, and a `run_command`
result repeats the command, so a worker that rewords a command (`ls`, `ls -1`,
`ls | cat`) gets past it even when the output never changes. Nothing stops a job
for that; the same output for many rounds is also what a job polling something
that has not changed yet looks like. Instead an Ollama worker counts its tool
rounds in a row whose tools returned the same output: the tool names and what
each returned, without the arguments and without the command `run_command`
echoes, in any call order. A round in which the tools all print nothing counts
as the same output too. From three such rounds, the job's row in the chat,
its line in `/workers` and its header in the job's detail view add `same output
for 12 rounds`. A persona on Ollama answering you is told the same on that
job (`output="same output for 12 rounds"`), with one sentence explaining it,
the `system/active-work-same-output.yaml` prompt, sent only while a listed job
carries it. A job left running by a cleared conversation is still listed
without its count. Different output clears it, and so does the end of the job.
It is never saved to the conversation, and it has no say in when the repeat
guard or any limit steps in.

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
