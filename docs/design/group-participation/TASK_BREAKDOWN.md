# Implementation handoff

The specs are ready to implement in the existing owners. This workitem does not
create a festival or modify production code. Ship gates below remain required;
passing a design probe is not production acceptance. Preserve unrelated worktrees
and preserve merged WI-471f30 changes. The WI-b51d66 human-speaker
routing fix is merged in the current base; preserve it when replacing semantic
address inference.

## 1. Core contracts and input ownership

**Owner:** obey-voice `brain`, `pipeline`, persona runtime, session/config schemas.

- Add typed audience, coordinator and participation decisions, ticket/state and
  budget types matching `contracts/`. Add explicit discussion stream directive
  and tools-disabled admission; keep legacy interfaces backward compatible.
- Define monotonic input/history/settings revisions at actual owning mutations,
  including canonical imports/clear/restore and externally committed worker notes.
  Ticket revalidation must use these counters, not `len(history)`.
- Add exactly-once canonical original-input recording for group/clarify outcomes
  and the initial-stream append suppression. Reuse existing continuity import
  and session persistence; interrupted/failed input cannot be silently lost.
- Add profile participation description and versioned session mode/member settings
  via existing schemas/editors. Validate length/UTF-8 bounds, preserve absent fields
  in old sessions, and never persist pending autonomous work.

**Acceptance:** replay contract valid/invalid fixtures; verify original human input
occurs once on first reply, all-pass, refusal and failed preparation. Verify old
single/direct/exact controls unchanged, old session round trips and future version
write refusal. Add failures before/after the session write-committed boundary.

## 2. Private inference and model qualification

**Owner:** obey-voice `brain` + `internal/persona/runtime`.

- Implement one audience classifier replacing existing semantic address request;
  preserve exact/`@id` precedence, human/agent identities and acoustic-name evidence.
- Implement a tools-free candidate probe using pinned full prompt plus snapshot
  projection without selecting a speaker, mutating canonical history, creating
  workers or loading model settings from a different persona's live binding.
- Implement coordinator selection with only eligible members and delivered
  history; validate schemas and semantic host constraints before any side effects.
- Propagate parent cancellation and short private deadlines. Disable new model
  warmups/retries inside discussion resource prep unless they reserve the same
  request budget; private snapshot creation cannot run compaction on the live brain.
- Use recorded prototype v2 prompt as a starting point. Qualify actual production
  prompt assembly and models rather than assuming raw transport results transfer.

**Acceptance:** import both audience corpora into real resolver tests; private
checks leave history, selection, working directory and persisted state bytewise
unchanged. Check timeout/invalid JSON/unknown key/bad references, shared aliases,
intermediaries, no eligible speakers and cancellation while queued in Ollama.
Private results cannot enter response/audio streams. Unsupported providers report
capability refusal without remote fallback. Run actual model quality gates below.

## 3. Invited discussion loop

**Owner:** pipeline continuation/admission/voice/text and persona Selector.

- Implement state transitions in DESIGN.md, first-opportunity queue and bounded
  tail selection with returning speakers. Do not replace existing legacy handoff
  behavior or its no-repeat/three-follow-up checks outside a discussion.
- Use existing proposal/commit boundary for speaker preparation/persistence,
  joining the interruption watcher and checking captured input immediately before
  commit. Add a second guard before releasing first text/audio.
- Disable tools, workers, route repair and extra answer regeneration for every
  discussion reply, including the first. Apply reply/deadline/request limits to
  all provider calls, including compaction; preserve attribution on partial output.
- End at the next response boundary for ready work and yield to the existing
  completion arbiter; do not clone worker-result scheduling or its ledger.
- Persist only stable settings and attributed transcript. Clear/close/mute/restore
  end and join private work; no autonomous speech after restoring a saved session.

**Acceptance:** drive real voice/text pipeline with stub model/TTS and the trace/
fence fixtures: A→B→A; full coverage with passes; all-pass; no consecutive repeats;
member failure; user input at every phase; successful stale proposal after cancel;
member/profile edit during preparation; request/response/time exhaustion; lost
sink; synthesis failure; ready work; clear/resume and reconnect. Assert identities,
selected binding, persisted transcript and absence of fake human/tool turns.

## 4. Controls, snapshots and native surfaces

**Owners:** obey-voice host dispatcher/TUI/CLI, embedded desktop Rust/Go bridge,
and obey-voice-apple Talk model/settings.

- Add capability-gated `set_participation`, correlated completion, discussion
  events and snapshot fields as specified in INTERFACES.md.
- Extend existing settings/persona selection controls for group membership and
  mode; existing Stop must be available during private checking/preparation too.
- Preserve old-client compatibility, host state revisions, discussion revisions,
  response-ID dedupe, attributed audio and ordinary selection semantics.
- Reconnect reconciles snapshot; controls never blindly replay an unconfirmed
  write. Offline CLI reports no active session rather than mutating defaults.

**Acceptance:** run real TUI terminal fixture and native isolated UI fixtures;
verify saved settings by reopening/resuming. Test stale snapshots, unknown
capability, old server, old client, unconfirmed update and interrupt during private
inference. Use `just test events`/`just ui-fixture` for embedded desktop and the
Apple project's current protocol/UI runner. No personal config mutation.

## 5. Bounded open participation

**Owner:** same engine discussion controller; clients expose only advertised mode.

- Implement one optional coordinator/check/reply at eligible ordinary completion
  boundaries; explicit legacy handoff wins. Exclude idle, background, greeting,
  recovery, work, narration, pending user input and already-added replies.
- No event timer or idle inference, no agent-authored mode change, no chaining
  from the unsolicited response. Mode/member updates cancel current discussion.

**Acceptance:** instrument request counts over idle periods, ordinary invited-mode
turns, open-mode yield/pass/contribution, explicit handoff and unrelated worker
completion. Prove zero extra calls for ineligible events and at most three added
requests for eligible events; verify real-user interruption and no automatic restart.

## 6. Release acceptance and enablement

**Owner:** engine + client maintainers; first qualification uses supported local
Ollama only. Keep feature off until these gates pass in production paths.

1. **Host correctness:** all contract/fence fixtures pass against real owners,
   including persisted input, immutable identity, stale output exclusion, tool
   prohibition and bounded work. Race-enabled focused Go checks pass.
2. **Audience quality:** all versioned positive/negative corpora pass on the
   intended model and actual assembled prompts; add blind unseen paraphrases and
   replay with at least two seeds. Any unintended group admission on incidental,
   quoted, third-party or ambiguous-address cases blocks enablement. Small corpus
   success is necessary, not a calibrated guarantee; retain model/version hashes.
3. **Dialogue quality:** recorded independent perspectives, relevant references,
   A→B→A revision/answer, passing and natural yield. A human reviewer assesses
   semantic correctness, repetition, forced dissent and fabricated agreement;
   string matching alone is insufficient. Model checks may assist, not certify.
4. **Measured resource/latency budget:** no idle polling; exact request bounds;
   warm audience decision p95 ≤4 s and private selection+check p95 ≤8 s over at
   least 20 representative opportunities. Measure cold start/queueing separately.
   Compare current single/direct/named-handoff first-audio baselines with the same
   models/voices and measure actual inter-speaker gap. If the budget fails, keep
   the mode disabled and optimize/requalify; do not remove the host bounds.
5. **Physical audio/user priority:** speak during every private/response stage;
   check cancellation, capture onset retention, actual next recognized utterance
   and no later stale speech. Recorded fixture input does not prove microphone
   barge-in. Keep room noise/echo controls in the production harness.
6. **Clients/persistence:** real terminal/native checks show state and Stop,
   attributed playback, reconnect and resumed settings. Resume starts idle.
7. **Open mode separately:** negative conversational corpus yields without social
   filler or irrelevant interjections; only eligible new information adds one
   response. If this gate fails, ship invited mode only and omit open capability.

Recommended execution order: 1 → 2 → 3 → 4, with 5 after invited behavior is
stable, then 6. Do not publish a release or enable settings from this design turn.
