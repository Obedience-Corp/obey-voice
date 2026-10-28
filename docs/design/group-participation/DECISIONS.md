# Design decisions

These are implementation defaults selected within the requested design scope,
not statements that Lance individually approved each setting. They are versioned
with the spec and may be adjusted from implementation evidence.

| ID | Decision | Rationale |
| --- | --- | --- |
| D01 | Extend existing semantic routing and pipeline continuation ownership. | Reuses exact controls, original-input preservation, speaker/voice binding and user priority. |
| D02 | `everyone` means all enabled personas in this conversation's catalog snapshot, including the selected persona. Named subsets/exclusions narrow it; implicit `you two` requires an unambiguous contextual pair. Maximum eight members per discussion. | Matches the current shared conversation roster. Exceeding the limit refuses group admission and asks for a smaller group; never silently samples members. |
| D03 | An everyone/each/named-joint invitation offers each member one private opportunity, then permits useful back-and-forth. An anyone-style invitation uses relevant-only selection. Passing satisfies an opportunity; it does not manufacture agreement. | Honors “everyone” without polling every member for a selective invitation. Coverage order is deterministic after semantic audience interpretation. |
| D04 | Start with coordinator + selected-persona private participation check, then normal streaming speech. During first coverage, select the next unserved persona without another coordinator call. | Limits fan-out and lets the persona decide whether it has a view; count all private calls as overhead. |
| D05 | After coverage, one semantic coordinator proposes one returning participant or yields. The selected persona checks the proposal against its own context before speaking. | Coordinator never impersonates other personas or dictates their opinions. A private pass ends the tail discussion; no search through all personas. |
| D06 | `participation_mode: invited` is the default. `open` is an explicit conversation setting; `off` preserves legacy handoffs. | Explicit group invitations work without enabling spontaneous interjections. Open participation remains bounded and occurs only at completed-turn boundaries. |
| D07 | Returning speakers may speak at most twice per discussion, never consecutively. No fixed personality roles or forced disagreement. | Enables A→B→A while bounding domination. Semantic relevance remains model-driven. |
| D08 | Group replies, including the first, have tools and worker spawning disabled. | A request for opinions must not execute several agents' plans; existing worker authority remains separate. |
| D09 | Ordinary selection remains on the last successfully admitted group speaker when a discussion finishes. No automatic restoration request. | Matches existing handoff follow-up behavior and avoids an extra model/voice swap. Private passes never change selection. |
| D10 | All autonomous discussion state is ephemeral. Save only per-conversation mode and explicit member configuration, plus normal attributed transcript. Resume never restarts agent speech. | Preserves session continuity without replaying stale tasks or a surprise debate. |
| D11 | Support local Ollama shared-history providers first. No remote fallback, new service or dedicated model requirement. | Current production history/prompt identity contract is established for this path. Clients advertise support only when fully wired. |
| D12 | Open mode permits at most one unsolicited contribution after an ordinary successful conversational reply, and never starts further autonomous handoffs. | Makes agents able to add a timely point while retaining a narrow autonomy scope. Broader back-and-forth needs a group invitation. |
| D13 | No background idle polling, scheduled re-checking or speculative full answers. | Eavesdropping means available attributed context, not every model continuously inferring. |
| D14 | Use bounded UTF-8 persona participation descriptions saved in profiles, default empty; never infer expertise from a voice or character name. | Coordinator gets only explicitly described perspective plus factual participation history. Empty cards remain valid; full persona prompt belongs only to its private check/response. |

## Fixed v1 engineering limits

These are policy ceilings, not measured performance promises. Validation records
observed text inference separately. Values are read once per discussion; shorter
parent deadlines always win. Initial release exposes mode/member controls, not
an unrestricted knob for removing ceilings.

| Limit | Value | Exhaustion behavior |
| --- | --- | --- |
| Members | 2–8 | A singleton collective invitation gets one tools-disabled response; over eight refuses group admission. |
| Private selection/check deadline | 10 s per call | Yield with a diagnostic and uncovered-member summary. Initial audience admission retains existing `persona_routing_timeout`. |
| Response count | `min(12, 2*N)` | End after current response; no silent renewal. |
| Per-persona responses | 2 | Candidate becomes ineligible; initial opportunity still may pass. |
| Coverage checks | N, once per initial member | Initial members processed in deterministic order; no re-check on a first-round pass. |
| Tail selection attempts | 4 | Ends even if fewer than four responses were produced. |
| Total model requests per discussion | 48 | Includes admission, checks, replies, continuation/compaction/recovery calls; reserve before every model request. |
| Autonomous wall time | 180 s | Cancel in-flight inference/playback through existing interruption path; record `budget_exhausted`. |
| One group reply | 160 output tokens; prompt requests ≤2 sentences | On output truncation mark degraded and end, without auto-recovery or compaction continuation. Pre-generation compaction is allowed within request/time budget. |
| Coordinator input | 8 recent delivered messages × 1000 Unicode characters, ≤16 KiB total JSON | UTF-8 safe trimming with `context_truncated`; initiating input up to 4096 characters retained separately. Overlong initiating input refuses group inference with visible reason. |
| Persona card | 256 Unicode characters, ≤1024 UTF-8 bytes | Validate at profile save; private prompts are not truncated into cards. |
| Private outputs | audience 256, coordinator 128, participant 128 tokens | Malformed/truncated result yields; never parse a partial decision. |
| Open-mode added work | 1 coordinator + 1 private check + at most 1 reply | No timers, retry or continuation loop. Same 10 s private deadlines and a 30 s total add-on deadline. |

With N members, coverage takes one admission plus N checks plus S spoken response
requests (`S≤N`), excluding counted compaction. Each useful tail response adds one
selection, one check and one reply. Limits enforce predictable growth without
hardcoding conversational phrases. Synthetic policy expectations live in the
contract fixtures; measured latency cannot be inferred from these formulas.

## Release decision

The implementation plan includes invited participation, bounded open-mode
interjections, TUI/CLI and native client controls. Release `invited` first if
open-mode false-positive or latency acceptance fails; preserve the specified open
contract as a separate disabled capability. A host must not advertise a mode that
has not passed its integration gate. Semantic/audio quality gates remain required
before enabling the feature in shipped builds; completing specs is not that gate.
