# Runtime audit

Audited 2026-10-28. Engine HEAD: `e7df8bf574c4997f8d6b7cf45dc3e2aaa340ce06`.
Apple client HEAD: `77ad42ac4482ea54ea75511014b224587250797c`.
Engine checkout was clean. Campaign and Apple checkout contain unrelated work;
this design changes only its own campaign workitem.

Handoff refresh: engine advanced during this work to
`865a245de354158f8bebadaa83f26239a758a5c8` (merged #266). Reviewed its changes to
group routing, exact controls and worker delivery; the continuation boundary is
unchanged. Leading `@id` now deterministically routes a substantive remainder,
not only a bare switch. Preserve that control before semantic classification.
Focused checks were repeated at this refreshed revision; see VALIDATION.md.

## Ownership and changes required

Paths below are relative to `projects/obey-voice` unless qualified.

| Owner | Current evidence | Specification consequence |
| --- | --- | --- |
| `cmd/obey-voice/cmd/root.go`, `serve.go` | Construct `personaruntime.Selector`; wire pipeline routing/handoff. | Wire one discussion controller into these same hosts; no second voice loop. |
| `internal/persona/runtime/routing.go` | Exact whole-input identity control precedes semantic address inference; original utterance is preserved. | Keep exact controls and one admission inference. Collective interpretation extends the semantic result. |
| `internal/persona/runtime/group_routing.go` | Catalog roster excludes inactive profiles. Resolver gets names/aliases and eight truncated conversational entries. Agent classification is gated by a name mention. Handoff excludes visited responders. | Add explicit audience resolution, bounded persona cards, contribution ledger, and discussion-aware next-turn proposal. Remove name-mention/visited restrictions only within a valid discussion. |
| `internal/persona/runtime/selection*.go` | Selector prepares fallible resources before publishing/persisting a selection; pipeline/host own serialization. History projection and prompt freezing are capability requirements. | Private candidate probes cannot select or persist a speaker. Revalidate discussion and candidate binding at the final selection commit. |
| `pkg/voiceagent/pipeline/continuation.go` | At most three additional responses; private classification after successful completion; interruption watcher joins before commit and capture is retained. | Extend this controller boundary with bounded discussion state; retain ordinary named-handoff behavior outside discussions. |
| `pkg/voiceagent/pipeline/{voice_turn,text_turn,admission}.go` | Continuation binds an immutable response envelope, appends no synthetic user utterance, and disables tools. | First collective response also needs an explicit tools-disabled discussion admission. Preserve exactly one original human turn. |
| `pkg/voiceagent/brain/{provider,ollama_history}.go` | `AgentContinuation` means a previous agent handed over the floor. Own replies project as assistant; other speakers are attributed shared context. | Add an explicit transient discussion directive; avoid telling the first speaker that a nonexistent agent handed over. Keep existing continuation semantics compatible. |
| `pkg/voiceagent/pipeline/continuation.go:completeAgentResponse` | Failed, degraded, interrupted and empty completions do not schedule another handoff. | Only successful delivered contributions advance discussion. Text delivery and voice delivery have different success predicates. |
| `internal/netapi/dispatch.go`, `docs/serve-protocol.md` | Serialized controls, session/epoch snapshots, response identity, monotonic host state revision, immediate `interrupt`, attributed audio. | Discussion revision is separate from host state revision. Add capability-gated controls/snapshot fields; use existing interrupt and audio reset. |
| `apps/desktop/src/backend/protocol.rs`, `src/app/events.rs` | Embedded Rust UI consumes engine bridge events; engine owns conversations and personas. | Add group state/controls through existing bridge; do not implement selection or dialogue scheduling in Rust. |
| Apple `Talk/TalkViewModel+Events.swift`, `+Connection.swift`, `+HandsFree.swift` | Accepts session snapshots and rejects older revisions; existing interrupt flushes playback. | Surface discussion state and settings behind capability probe; preserve revision and response-ID filtering. |
| `pkg/voiceagent/decision` | Optional worker routing proposals with separate provider/admission rules. | No reuse of its worker-tier answers for participation. Shared transport extraction is optional, not required. |

## Existing checks worth preserving

- `internal/persona/runtime/group_routing_test.go`: full utterance preservation,
  uncertainty/unavailability, collision failures, compacted context transfer,
  proposal failures preserving selection/disk.
- `group_wire_test.go`: projected wire ownership and persisted identity.
- `pkg/voiceagent/pipeline/continuation_test.go`: no fake user, immutable admission,
  legacy limit/no-repeat, degraded/background/interrupted turns, optional failures.
- `onset_test.go`: speech cancels classification, retains onset, prevents successful
  stale proposals committing, and joins the watcher before speaker selection.
- Desktop `just test events` and isolated `just ui-fixture`; native Apple protocol
  replay is required in its owning repository during implementation.

Run focused Go tests directly: the engine just runner has whole-suite `test unit`
and internal-package `test pkg` recipes, but no recipe matching this cross-package
selection. No new runner or production dependency is needed for this design.

## Evidence boundaries and adjacent work

The existing convoflow runner loads personal config before overlaying some knobs;
do not invoke it unmodified as an isolated proof. Production acceptance must add
an explicit disposable configuration entry point or use existing fully isolated
runtime tests. The design probe uses only synthetic text and loopback Ollama.

`WI-471f30` completed and merged during this design: worker result speech cleanup,
menu lifecycle and persona mention completion. Preserve those owning seams; do
not duplicate its command palette or formatting guard. `WI-b51d66` now tracks
known human speakers being incorrectly routed as personas. Incorporate that fix
before replacing semantic addressee inference, preserving human/agent account
attribution and refusal of truly unknown explicit targets.

No existing code proves collective invitation classification, returning speakers,
private passes or open participation. Local model probes can evaluate semantics
and request costs, not physical microphone priority or true first-audio latency.

## Review branch refresh

The review branch starts at `239c3cf8b2fb5bd64a8eed5c435938b3a865c74d`,
which includes #267 and #268. The previously pending human-speaker fix is merged.
The baseline measurements above retain their original revisions; do not describe
them as measurements of this newer source. This documentation PR changes no
runtime behavior and does not requalify the recorded prototype models.
