# Background tasks through Obey

Opt-in background tasks let the Ollama voice conductor queue a managed worker,
acknowledge the request immediately, and keep answering unrelated questions.
A finished task's compact brief enters the next conversational turn. Obey owns
execution, policy, budgets, durable results, and cancellation.

## Required integration

This consumes the managed delegation **CLI schema version 1**, including
`obey delegation capabilities`, `start`, `await`, and `cancel`, from the
voice-delegation-bridge change in Obey. OA0002 alone is insufficient: it shipped
managed execution internally, but its external start/status RPCs did not expose
result text or cancellation. A successful OA0002 festival does not establish
that this voice client can use the complete lifecycle.

The voice build also needs WI-b94dad's `AddDelegationNote` projection boundary.
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

Ready briefs join the current user turn's context group, so context-budget
trimming cannot evict them as an older exchange. They are acknowledged only
after a successful final model request that included the note. A failed or
interrupted turn keeps the brief pending for retry. Compaction/meta turns do
not consume results. Delivery means the conductor received the brief; the
model chooses how to include it in its spoken reply.

Interrupting speech does not cancel background work. Explicit cancellation,
the configured task timeout (measured from queueing), and voice-session cleanup
request cancellation through Obey. A cancellation request is not presented as
proof that the worker stopped. Admission runs outside the turn context and
retries with the same idempotency key when its outcome is uncertain. Shutdown
may wait for admission to resolve so a newly created child can be canceled;
three admission attempts are bounded at 45 seconds each, followed by a bounded
cancel request. Unconfirmed admission retains its ticket and blocks further
work until the operator checks Obey and restarts the voice session.

## Remaining WI-ebe14b scope

This is the managed-worker integration with **next-turn delivery**. It does not
implement unsolicited speech while the user is idle, delivery immediately after
an already-running reply, remote A2A targets, worker continuation/input-required
UX, or recovery of voice tickets after process restart. Those are separate
follow-ups; the complete V5 idle mini-turn acceptance gate remains open. Obey's
durable results remain available independently of this process.

The original seed's one-tool restriction is revised: explicit cancellation and
cached status need model-callable controls. All three names already belong to
Obey's vocabulary; no blocking wait is exposed. Hardware speech and a real
provider-backed worker are not simulated by the unit tests.
