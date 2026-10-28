# Group participation specification v1

**WI-86d50e.** Normative implementation contract. “Must” denotes an invariant.
See [AUDIT.md](AUDIT.md) for current code, [DECISIONS.md](DECISIONS.md) for
defaults/limits, [INTERFACES.md](INTERFACES.md) for integration, and
[VALIDATION.md](VALIDATION.md) for evidence. Implementation and shipping gates are
separate from completing this specification.

## 1. Audience admission

Keep existing exact whole-input identity controls first. For other finalized
input, extend today's private semantic addressee inference into one audience
inference; do not add a second classification call. Store original user input
once, unchanged. Interpret conversation meaning, not a plural phrase list.

Preserve leading `@id` deterministic routing, including its substantive remainder,
from the refreshed engine. Classifier context distinguishes known human speakers
from agent participants: preserve human labels/accounts, use a separate human
key namespace, and never put a human in the eligible agent roster. Integrate
WI-b51d66's existing-speaker fix rather than creating another identity resolver.

On group/clarify admission, append the original user turn through the existing
continuity import/persist boundary even if everyone passes or preflight refuses.
`DiscussionDirective.input_already_recorded=true` suppresses the provider's normal
user append on the initial group reply. It still identifies a real initial human
request, not an agent handoff. Persistence failure prevents private participation
and speech. Current/direct paths keep existing append behavior. User-input events
are not a substitute for persisted canonical input.

| Meaning | Decision |
| --- | --- |
| Ordinary follow-up, incidental mention, quotation or hypothetical about agents | `current`; ordinary selected responder. |
| One addressed agent, including “A, ask B…” | `direct` to A; preserve intermediary and legacy handoff semantics. |
| “Everyone,” “both of you,” named agents jointly asked for views | `group`, `coverage: all`; every invited member gets one opportunity. |
| “Anyone have another perspective?” | `group`, `coverage: relevant`; select a useful member or yield without polling all members. |
| Unknown target, shared addressed alias, unresolved contextual audience | `clarify`; visible host notice, no invented selection. |

Requests/responses use [contracts/decisions.schema.json](contracts/decisions.schema.json).
Model participant keys (`p0`, `p1`, …) map to profile IDs only on the host. Input
contains public names, aliases and bounded persona cards, never storage IDs,
credentials or tools. Model-generated keys must resolve in the frozen roster.

Default conversational membership is enabled catalog personas. An optional
conversation member setting narrows it independently of the selected responder.
Explicit subsets and exclusions apply before group admission. Resolve “you two”
only from an explicit recent audience or an otherwise unambiguous contextual
pair; merely picking the last two speakers is insufficient if another pair is
plausible. Agent messages cannot expand the user-admitted audience or mode.

Named missing/inactive/excluded/unsupported members refuse admission. Default
“everyone” also refuses if a broken catalog entry has unknown enabled status;
never silently omit a requested member. No automatic downloads or repairs.
Two to eight members form a discussion. One member gets one tools-disabled
response and yields; zero clarifies; more than eight asks for a smaller group.
Order first-opportunity coverage by selected responder if invited, then stable
profile-ID order. Ordering follows semantic resolution and implies no expertise.

## 2. Ownership and revision fencing

The existing pipeline owns one `DiscussionController`. Persona runtime proposes
audience/candidates; Selector prepares resources and commits identity/persistence
at the serialized host boundary. No second response loop or speech scheduler.

Pin a coordinator transport/model/options binding from the initiating selected
local Ollama brain. Its private system instruction is the versioned coordination
policy, not that persona's speaking prompt. Keep this coordinator binding for the
whole discussion as response personas/models change; otherwise selection quality
would silently change with the last voice. Candidate checks/replies use their own
profile models and full persona prompts. No dedicated coordinator model or remote
fallback is introduced. A new human input gets the then-selected binding.

Ephemeral `DiscussionState` contains:

- Random discussion ID; existing session/epoch; initiating input sequence;
  discussion, input, history and settings revisions; roster hash and selection generation.
- Frozen member keys → profile IDs, binding hashes, labels/cards; mode/coverage;
  original request; start/deadline; inference and response counters.
- Remaining first-opportunity queue; per-member delivered count; last successful
  delivered response/speaker; phase, candidate and terminal outcome.

A private `DecisionTicket` captures these revisions/hashes, candidate binding,
random attempt ID and expiry. Models cannot supply revisions. Revalidate after
inference, after resource preparation, under final selection commit and before
first text/audio release. A mismatch discards without re-inference or persistence.
I/O inherits the owning cancellable conversation context; do not hold read locks
during inference or load work.

New user input/onset/interrupt, external persona selection, profile/member/mode
edit, unrelated transcript mutation, clear/close/restore invalidate pending
tickets. Discussion-owned successful selection/history append updates revisions
and issues the next ticket without cancelling itself. Roster edits end this
discussion; do not introduce members mid-flight.

## 3. State machine

| State | Event | Transition/effect |
| --- | --- | --- |
| `idle` | Finalized input | `admitting`; supersede previous discussion, use existing route. |
| `admitting` | `current`/`direct` | Return ordinary path; may later qualify for one open-mode add-on. |
| `admitting` | Group + preflight succeeds | `choosing`; freeze members and budgets, append original user once. |
| `admitting` | Clarify/preflight failure | `ended: refused`; host notice and original-input retention, no retry. |
| `choosing` | Unserved `all` member remains | `checking` next queue member; no coordinator call. |
| `choosing` | Relevant coverage or initial coverage finished | One coordinator `select` → `checking`, or `yield` → `ended`. |
| `checking` | Private `pass` | Consume opportunity; next unserved member → `choosing`; otherwise `ended: no_contribution`. |
| `checking` | Private `speak` | `preparing`; retain transient intent and response references. |
| `preparing` | Prepared resources + final ticket valid | `responding`; commit existing selection/envelope, reserve reply request. |
| `responding` | Successfully delivered response | Increment counters, mark opportunity, advance history revision → `choosing`. |
| Any active | User onset/typed input/interrupt/external selection | Cancel inference/playback, invalidate tickets → `ended: interrupted` or `superseded`; user owns next floor. |
| Any active | Clear/close/restore/member/profile/mode mutation | End `superseded`; join work before cleanup. |
| Any active | Time/request/response limit | `ended: budget_exhausted`; cancel running output, preserve truthful partial state. |
| Any active | Invalid decision, timeout, prep/model/delivery error | `ended: failed`; diagnostic and uncovered-member summary. |
| `ended` | Finalize | Publish terminal state, discard pending work → `idle`; keep last admitted speaker selected. |

Coverage means an opportunity, not compulsory speech. Pass consumes it; cancelled
or incomplete check/reply does not. Private passes never select/persist a speaker
or append dialogue. A response failure after admission may leave that speaker
selected, just like an ordinary failure; audio already delivered cannot roll back.

## 4. Selection and actual persona participation

After coverage, the coordinator gets bounded delivered history, original request,
eligible public members/cards, ledger and budgets. It returns one candidate and
reason enum or yields. It never writes another persona's opinion. Eligible tail
members exclude the last speaker and members with two delivered replies. No
eligible member → yield without inference. Invalid selection ends the discussion
rather than asking the model repeatedly to repair it.

The selected persona performs an isolated private check using its pinned full
persona prompt and existing projected bounded working context. It returns
`speak`/`pass`, contribution reason, a ≤240-character intent, and up to two
delivered-response references. It may reject the coordinator premise. Ask for a
useful perspective, answer, correction, clarification, challenge, agreement with
new substance, or revision; pass on filler or repetition. No forced disagreement,
character-based expertise assignments, or fabricated numerical confidence.

`answer`, `correction`, `challenge`, `revision` require references that resolve to
delivered replies in this discussion. Returning speakers need one of these reasons
and a reference. An initial independent perspective needs no reference. These
checks prove grounded identity, not semantic truth/novelty; evaluate those live.

Add a narrow private-provider capability for a candidate prompt/projection check.
Capture a snapshot without `Select` or mutating the live provider's history. Never
invoke the normal mutating `Stream` as a private classifier. After a valid check,
generate the actual reply with the selected persona's existing stream and voice;
the intent is transient guidance, not a saved assistant message.

Add `DiscussionDirective` to stream options: initial response retains the original
human request; subsequent replies append no fabricated user and refer to actual
delivered discussion. Do not tell the initial speaker another agent handed it
the floor. Every group reply, including the first, disables tools and worker
tiers/spawning. Exclude ordinary worker routing, notifications, local greetings,
repeat-regeneration and truncated-response continuations. Pre-generation
compaction is allowed and charged to the shared request/time budget; output
truncation degrades and ends this discussion. Full-response generation is never
speculative or prepared for all members in advance.

## 5. Open participation

`off` preserves ordinary direct/named handoffs. A collective request gets a visible
“group participation is off” outcome. `invited` is the default; explicit user
group invitations enable bounded discussion. `open` additionally permits one
unsolicited contribution after an ordinary successful conversational reply.

Open eligibility requires supported local providers, at least two members, no
pending input, no active discussion or named handoff, and no work delivery,
narration, background, greeting or recovery reply. A deliberate legacy handoff
has precedence and excludes an open add-on after that chain. Exclude the previous
speaker. Run at most one coordinator, one candidate check and one reply. The
coordinator must identify a concrete relevant correction, unanswered question or
new point; availability, name mentions and social agreement must yield. A pass
ends silently. The added response cannot start further handoffs, even if it names
another persona. No timers, idle polling, mid-sentence interjection or renewal on
silence. User speech/controls always preempt.

Mode is set explicitly through the conversation control/UI in v1. There is no
spoken mode-control intent in the v1 schema: such requests follow the normal
responder path, which may explain the setting but cannot change it. Never let
agent content, quoted examples or inference silently change mode. Conversational
mode controls can extend the audience contract later with dedicated fixtures.
Open-mode capability stays disabled in
shipping builds until its false-positive and latency integration gates pass.

## 6. Delivery, failure and persistence

Only a fully successful delivered response drives another decision. Text requires
canonical publication and successful host persistence boundary. Voice additionally
requires the ordinary pipeline's configured delivery sink and completion signal.
Inference completion alone or recorded audio without an accepted sink does not
prove speech was heard. Freeze coordinator history after delivery; exclude earlier
canonical appends that are still pending playback. Do not remove incomplete
canonical history: preserve its existing flags and attribution.

Candidate projections must mark any generated-but-undelivered group content as
incomplete context, never as something another speaker heard. Both coordinator
and participant check references use the delivered ledger. Projecting a new
candidate uses existing continuity snapshots; a private check must not compact,
append to or otherwise mutate the live brain. If its full pinned prompt cannot
fit the safe context budget, end visibly rather than truncating persona rules.

No automatic retry for timeout, invalid JSON/reference, unavailable member,
synthesis failure or budgets. Keep prior completed replies and emit one terminal
outcome with public labels for uncovered members. Never call private passes
agreement. If all pass, host info notice only; errors are host notices, not persona
opinions. Partial failures do not create a retry queue or resurrect speech.

Background results keep the existing work ledger and authority. They never act as
participation bids or start discussion. Work ready during discussion ends it at
the next response boundary with `work_ready`, letting the existing completion
arbiter deliver; no parallel narration or new queue.

Save normal attributed transcript and optional conversation mode/member settings
using the existing versioned session store. Do not persist autonomous state,
queues/tickets, private checks or intents as dialogue. Restoring a saved session
starts idle. Clear/close cancel/join before cleanup. Muting ends autonomous speech;
unmuting does not resume it. A transport reconnect to a surviving engine receives
its actual live snapshot; restoring a saved conversation never restarts it.

## 7. Invariants and rollout

1. At most one response speaker, one private request and one prepared candidate.
2. Every spoken response has an existing immutable identity/voice envelope.
3. Exactly one human turn for the initiating request; zero fake users for checks.
4. No commit or output from an expired ticket or an invalidated conversation.
5. Every model request reserves shared budget before dispatch, including compaction.
6. No contribution creates tool/worker/spending authority.
7. Completed or interrupted discussion cannot restart without new human action.

Implement and verify [TASK_BREAKDOWN.md](TASK_BREAKDOWN.md). Prototype schema and
model checks establish specification consistency and feasibility only. Physical
audio priority, first-audio timing, real pipeline state fencing, persistence and
native clients require the listed production gates before release.
