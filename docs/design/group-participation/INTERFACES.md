# Engine, provider and client contracts

All names below are proposed additions to current owners, not existing APIs.
Implement additive versioned fields through the project's schema/code-generation
conventions. Production ownership is mapped in AUDIT.md.

## Internal engine boundary

| Boundary | Request | Result / side effects |
| --- | --- | --- |
| `AudienceResolver.ResolveAudience(ctx, request)` | Frozen public roster keys/names/aliases/cards, latest original text, bounded attributed history, explicit recent audience | Audience decision; no history, selection, tools or worker side effects. Replaces one semantic addressee call. |
| `DiscussionPolicy.ProposeNext(ctx, request)` | Original request, delivered history/response IDs, eligible keys, coverage/counters, mode, remaining budget | `select` or `yield`; does not load/select a persona. |
| `ParticipationProbe.Check(ctx, request)` | Candidate's pinned prompt and projected working-context snapshot, immutable directive, delivered response references | `speak` or `pass`; no canonical append or live-brain mutation. |
| `DiscussionController.Next(ctx, completion)` | Current delivery outcome and host revisions | Side-effect-free candidate proposal with `DecisionTicket`; serialized `Commit` delegates to Selector and ordinary response admission. |
| `DiscussionBudget.ReserveRequest(ctx, purpose)` | Admission/check/coordinator/reply/compaction purpose | Atomic reservation or refusal, shared by every request in this discussion; no hidden recovery calls. |

Interface implementations must preserve context cancellation on all I/O and wrap
errors with purpose and existing classifier failure kinds. Payloads are copied
before asynchronous inference. At most one private request is in flight. Candidate
resource preparation is cancellable and joined before cleanup. A private probe
gets a snapshot, not access to mutate a live `brain.Provider`.

`StreamOptions.Discussion` is optional and contains `discussion_id`, phase
`initial|continuation|open_addition`, original input sequence, `input_already_recorded`, transient intent,
validated response references and the shared budget reservation hook. Existing
`Identity`, canonical history and response envelope remain authoritative. Existing
`AgentContinuation` remains unchanged for legacy handoffs; discussion generation
uses its explicit phase to avoid fabricating a previous agent invitation.

Host `ResponseAdmission` carries optional discussion metadata and explicit
`ToolsDisabled`; do not rely only on `isContinuation`, because the first group
response must be tools-disabled too. Worker tiers and route tools are absent.
For private classifiers use separate strict schemas, no tools or transient skill
injection. Public text/context remain data, including malicious instructions to
alter the classifier. Validate shapes and host semantics before any side effects.

## Machine decision payloads

The schema bundle has independent `$defs.audience`, `$defs.next`, and
`$defs.participant`. Pass the selected definition to a structured-output model;
the bundle's root `oneOf` is for offline validation, not an extra model decision.
All fields are required and additional properties forbidden. Model-visible inputs:

```json
{
  "participants": [
    {"key":"p0","name":"Jarvis","aliases":[],"card":"Implementation tradeoffs"},
    {"key":"p1","name":"Samantha","aliases":[],"card":"Usability and communication"}
  ],
  "current":"p0",
  "latest":"What does everyone think about this?",
  "history":[],
  "explicit_recent_audience":[]
}
```

Next-speaker requests replace `latest/current` with `original_request`,
`eligible_keys`, delivered `{response_id,key,text}` records, opportunity/spoken
ledger, mode and remaining counters. Private candidate checks add that candidate's
full pinned prompt/projection outside untrusted conversation data; full prompts
never go to the coordinator. Prototype `prompts.json`/`probe.py` supply synthetic
cards and text, not a production prompt-projection implementation.

After JSON Schema validation the host must also enforce:

- `current`: no members, no coverage, no unresolved names, reason `none`.
- `direct`: exactly one member, coverage `none`, reason explicit/contextual.
- `group`: at least one member, coverage all/relevant, reason explicit/contextual.
  A collective invitation with a singleton roster retains group intent and the
  host runs exactly one tools-disabled response, then yields. A normal explicitly
  named single listener is `direct`, preserving its ordinary tool behavior.
- `clarify`: no members/coverage; reason unknown/ambiguous and bounded public names.
- Member keys must exist. Apply roster and setting eligibility on host. Exact
  controls retain deterministic alias collision refusal. A key alone cannot
  prove a semantic classifier interpreted a shared alias or unknown name
  correctly; these are mandatory model-qualification cases, not properties
  established by schema validation. Do not claim host validation solves intent.
- `yield`: empty member and `no_contribution`. `select`: a member in eligible keys
  with another reason. Preserve at-most-two/nonconsecutive speaker constraints.
- `pass`: `no_contribution`, empty intent/references. `speak`: nonempty intent and
  meaningful reason. References resolve to delivered response IDs; returning
  speakers require answer/correction/challenge/revision and nonempty references.
- A decision is usable only with its original live unexpired host ticket. Schema
  compliance alone does not prove audience correctness, novelty or authorization.

## Configuration, controls and persistence

Engine feature switch `group_participation_enabled` defaults false during
implementation. After release acceptance it may default true. `open` remains
unavailable until its independent gate passes. Mode default is `invited` whenever
the engine feature is enabled. Per-conversation `participation` is optional:

```json
{"version":1,"mode":"invited","members":null}
```

`members:null` means current enabled catalog. An explicit array contains 1–8
unique configured persona IDs; it need not contain the selected responder.
Ordinary direct/exact selection continues to work outside that subset but does
not enlarge it. Updates apply at a serialized conversation boundary, end current
discussion and advance settings revision. Invalid update leaves prior settings
intact. Unknown/newer setting version refuses writes without dropping old data.
Absent settings on old sessions resolve to the feature's default; no schema
changes to historical dialogue are required. Save through the existing session
format migration, not a second settings file or scheduler store.

New WebSocket/bridge request, guarded by capability `group_participation_v1`:

```json
{"type":"set_participation","request_id":"req-1","mode":"invited","members":["jarvis","samantha"]}
```

All fields are required; `members` may be null. Request ID length/validation and
completion correlation follow `set_persona`. Success emits
`participation_set {request_id,session_id,conversation_epoch,settings_revision,participation}`;
refusal uses the existing correlated error shape with no state mutation.
`open` additionally requires `open_interjections_v1`. Disconnected clients do not
blindly retry an unconfirmed write: reconcile snapshot first. Engine text/voice
input never interprets an agent's output as a control frame.

Existing `interrupt` cancels the entire autonomous discussion, including private
inference and queued output; no new stop endpoint. TUI `/participation` opens
existing-style controls for Off / Invited / Open and member selection, with a
visible Apply action. CLI `obey-voice participation show|set` uses the running
instance's existing authenticated local control path. Offline CLI reports no
active conversation and does not rewrite global persona defaults. Add these
commands to existing help rather than a second persona manager.

## Events, snapshots and UI

`discussion_updated` is an additive reliable control event, emitted on phase,
membership/counter or terminal transitions. It includes session/epoch,
discussion ID/revision, phase, mode, public member IDs/labels, current candidate
ID or null, delivered count, uncovered IDs and terminal reason. It never exposes
private check text/intents, prompt content or a fabricated assistant response.

Terminal reasons: `yielded`, `no_contribution`, `open_complete`, `refused`,
`interrupted`, `superseded`, `budget_exhausted`, `failed`, `work_ready`. Error detail
uses bounded existing diagnostics. Terminal event carries last summary; next idle
snapshot has `discussion:null`. Correlation/metrics retain the ID without saving
private dialogue into transcript. Existing response events gain optional
`discussion_id`; envelope/response ID and audio generation fence still own output.

`session_snapshot` adds optional `participation` and `discussion` under capability
probe. A snapshot is authoritative and ordered through existing dispatcher
serialization. Discussion revision is monotonic per ID; host `state_revision`
retains its existing distinct role. Clients reject stale session/epoch/ID/revision,
and reconnect reconciles via snapshot rather than replaying a local queue.

The CLI/TUI, embedded Rust desktop and Apple client show mode, included personas,
who is currently preparing/speaking, and the existing Stop control during private
inference too. Use host info for uncovered members/failures; private passes need
not create chat bubbles. Old clients ignore new optional fields, keep existing
identity/audio behavior and can still interrupt. They cannot configure new modes.
The Rust bridge forwards canonical engine state; native clients never arbitrate
participation or infer speaker identity from the most recent selection.

## Diagnostics and acceptance instrumentation

Record input/audience decision kind, discussion ID, request purpose/count,
queue/load/prefill/generation durations, context truncation, eligible member count,
delivered/uncovered counts, pass reason enum, ticket invalidation reason and
terminal reason. Ordinary diagnostics omit user text and private intents. Existing
opt-in recording policy controls any retained content. Transport names and models
are facts, never permission grants. Measure time to first audio and inter-speaker
gap from named host milestones; text-only inference duration is not either metric.
