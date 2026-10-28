# Obey Voice: selective group participation and agent dialogue

**Workitem:** `WI-86d50e` (`design-obey-voice-group-participation-2026-10-28`)
**Updated:** 2026-10-28
**Status:** Design specification for review. Production behavior is unchanged; model/audio/client release gates remain open.
**Projects:** [obey-voice](../../../) (engine and embedded desktop), [obey-voice-apple](https://github.com/Obedience-Corp/obey-voice-apple) (native Talk client).

## User outcome

When Lance asks “What does everyone think about this?”, agents can give their
own views and respond to one another, including answering questions, disagreeing,
passing or revising an earlier position. Context determines the audience and
intent; conversational phrases and dialogue are not hardcoded. User speech
always takes priority. Open participation can allow a useful unsolicited addition
at completed-turn boundaries without continuously running every idle agent.

Source: Lance's feature discussion and request to design/specify it on 2026-10-28.
Defaults below are design choices within that scope, not individually approved
user preferences. Production implementation/release is the next distinct task.

## Specified behavior

- Extend existing semantic routing, Selector and pipeline handoff commit boundary.
- Everyone/each/named-joint invitations offer all invited members a private
  opportunity; anyone-style invitations select for relevance. Passing is valid.
- Select one candidate at a time. Each persona checks its own contribution and
  generates its own response; the coordinator cannot impersonate their views.
- Permit nonconsecutive returning speakers, at most twice per discussion, under
  explicit response/request/time budgets. Disable tools/workers for every reply.
- Use `invited` mode by default when the feature is enabled; optional `open` adds
  at most one useful reply after an ordinary response, with no idle polling.
- Fence every private proposal against input/history/settings/identity changes;
  preserve canonical input once, immutable speaker identity and delivery outcomes.
- Save stable mode/member settings and ordinary transcript. Saved-session restore
  starts idle; private checks and pending autonomous speech do not become dialogue.

## Structured records

| Record | Contents |
| --- | --- |
| [DESIGN.md](DESIGN.md) | Audience rules, state/transition table, selection/checks, interruption, failure and invariants |
| [DECISIONS.md](DECISIONS.md) | Chosen defaults, rationale and exact engineering limits |
| [INTERFACES.md](INTERFACES.md) | Internal provider/controller contracts, stream directives, settings, controls, snapshots and UI |
| [AUDIT.md](AUDIT.md) | Current source ownership, reusable seams and adjacent work |
| [contracts/](contracts/README.md) | JSON Schemas, audience corpora, decision examples, lifecycle/fence fixtures and executable checks |
| [VALIDATION.md](VALIDATION.md) | Actual checks, local model results, timing evidence and limitations |
| [REVIEW.md](REVIEW.md) | Consistency review and required production proof |
| [TASK_BREAKDOWN.md](TASK_BREAKDOWN.md) | Ordered implementation slices, owners, acceptance and release gates |

This review snapshot belongs to campaign workitem **WI-86d50e**. Its audit,
contracts, decisions and raw synthetic evidence are included here so reviewers do
not need access to the campaign workspace. Historical context: WI-2528db shared
personas, WI-471f30 merged conversation fixes, and WI-b51d66 human-speaker routing.
The human-speaker fix is now included in the PR's base; preserve its attribution
and unknown-target behavior during implementation.

## Verification and limits

Artifact validation passes: three decision schemas and three host schemas;
valid/invalid decision examples, seven trace witnesses and fifteen commit-fence
cases. Existing targeted runtime, pipeline and brain checks pass. These fixtures
validate specification consistency, not an implemented discussion scheduler.

Local Qwen3.8 v2 matched 15/15 inspected audience cases and 5/6 separate unseen
cases; the remaining audience omission is retained as a failing release gate.
Two smaller models performed worse in the first experiment. No model is declared
release-qualified. HTTP request timings are recorded separately from unmeasured
first-audio and between-speaker latency. No microphone/speaker, live personal
config/session or production-client acceptance was exercised.

## Handoff

Specs and design validation are complete, with negative results recorded. Start
with TASK_BREAKDOWN.md slice 1 in an isolated engine worktree, keeping the feature
disabled. Implement real owner boundaries and qualify the production prompt/model
before enabling invited mode; open participation has its own later gate. This PR contains specifications, fixtures and text-only experiment tools. The
campaign workflow is complete; runtime implementation is a follow-on change.
