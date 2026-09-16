# Completion playback admission — design for review

Status: design only against `206cba2f`. The user has already authorized implementation. This design does not yet define complete safe runtime behavior: unknown disposition and source coverage remain unresolved under the current full-duplex contract. A question about an explicit half-duplex fallback is pending; no answer has changed that contract. Silent discard, automatic promotion, and a blocked-capture flow each change observable behavior and do not resolve these technical gaps.

## Concrete failure

Completion population sample 013, `completion-20260916T013122Z`, started local-speaker playback at `01:31:29.287306Z`. At `01:31:30.548670Z`, about 1.261 seconds later, its completion lease was revoked. Public delivery became interrupted without a `SpeakingInterrupted` event or an external stimulus. Raw-capture ASR contains assistant words; whole processed-capture ASR is blank, which does not prove silence or identify the exact live admission frame.

The reachable path is `offline_loop.go` → `noiseGate.decide` → `speechTracker.observe` → `pauseController.boundary` → `RevokeCapturePermit` → `withCompletionLease` cancellation. One admitted frame sets `track.detected`; pause ownership revokes immediately. The streaming loop has equivalent ownership and can already have fed the recognizer. The separate `interruptController` requires the existing arm window, neural speech, gain-normalized audibility, completed-segment/sustained-callback qualification, and speaker policy. Its callback cadence is not interchangeable with STT's 1600-sample reads. This failure is already beyond the 600 ms guard: extending or copying the timer is not a repair.

## Proposed narrow interfaces and owners

Names below specify responsibilities for review, not public API commitments.

- `audio.PlaybackAttempt` may carry a pointer to a small `FirstRenderBoundary` containing its immutable attempt ID/capture epoch and a once-written first device-write timestamp. The existing sink callback publishes it only inside the valid playback fence when PCM is actually copied. Publishing performs no STT call, coordinator call, allocation, or waiting. All segments share the attempt boundary. Queued synthesis and an eventually consumed `SpeakingStarted` event are insufficient boundaries.
- An optional `stt.CompletionPlaybackCapture` extends `CompletionCapture` with `BindPlayback(ctx, token, binding)`, `ConfirmPlaybackInput(ctx, token, attemptID, evidence)`, and `FinishPlayback(ctx, token, attemptID, outcome)`. Requests are processed and acknowledged by the existing STT loop at a frame boundary. The binding contains the first-render boundary and capture-generation identity; it carries no mutable detector shared with pipeline.
- `pipeline/completion_lease.go` installs the binding after the lease claim and before generation/playback can start; it retires only that binding on every exit. Unsupported adapters must keep their existing completion-deferral behavior.
- During a completion, `response_stream.go` forwards the existing qualified barge request to the STT owner and awaits its matching acknowledgement before the completion interruption path proceeds. Ordinary responses retain their existing controller. Do not let the completion watcher and paused STT independently commit competing ownership decisions.

A confirmation includes attempt, pause token, generation, and capture source coverage. Today's `interruptRequest` has only reason/time and its subscription supplies PCM without positions. A typed metadata-preserving subscription, or equivalent evidence from the existing capture owner, is required to match confirmation to the correct buffered frames. Do not manufacture a sample offset from request delivery time or run a second watcher with different thresholds.

## First-render ordering and pre-generation input

`audio.Frame` now preserves `StartedAt`, `CaptureSpans`, device generation, and gaps through the ring. Use those existing values: their times are callback estimates, not hardware clocks. Compare capture coverage with the sink boundary, not the time the STT goroutine reads it. Frames wholly before first render retain ordinary admission and exact PCM, even if read afterward. Speech already committed before render continues as the same utterance and immediately revokes the lease; binding playback must never clear it. This preserves PR194's pre-generation behavior.

A span crossing the boundary, missing timing, loss, or a device-generation change is explicitly uncertain. Neither side may silently classify the entire span as echo. The exact boundary uncertainty must be retained in the candidate/evidence and covered by deterministic tests.

## Candidate state and promotion

Playback-period evidence is provisional before it reaches `track.detected`, committed `heard` samples, recognizer finalization, or `HasCapturedInput`. Rejected echo must not leave a latched tracker or streaming-recognizer ghost that becomes speech at resume. Keep the existing STT session as sole capture consumer; reuse its detector/recognizer ownership, with provisional state isolated from committed utterance state.

The existing barge policy supplies positive interruption qualification, not proof of speaker identity or an exact near-end onset. A matching confirmation can authorize an interruption; transcription promotion also needs defined source coverage. Retain the candidate's original PCM and positions, including the pre-confirmation opening, and transfer it once. Do not crop from first neural confirmation, replay already committed PCM, or prepend all earlier assistant audio merely because a later user barge was confirmed. If evidence cannot identify coverage without these errors, promotion remains unresolved.

Candidate storage must be bounded by the existing effective utterance limit (30-second default), not an invented unbounded response archive. Reaching the bound, losing frames, or mixing generations marks unresolved input; it must not silently evict the opening and report successful retention. The handling of that unresolved outcome needs the same terminal-policy decision below.

## Finish, failure, and unknown audio

| Situation | Safe established behavior | Remaining decision |
|---|---|---|
| No PCM ever rendered | Preserve ordinary capture admission and resume the same session. | None, provided token and generation still match. |
| Matching confirmed input with valid source coverage | Commit exact candidate once, revoke matching permit, retain normal transcript ownership. | Coverage must be proven, not inferred from delayed neural onset. |
| No provisional input | Finish playback and resume normal capture without stale state. | Confirm both loop implementations agree on what is provisional. |
| Completion ends/cancels/fails with unconfirmed candidate | Classify it as **unknown**, keep its bounded PCM/provenance separate, and do not mark it admitted speech or successfully retained user input. | The design has not established how resume, discard, or a blocked-capture flow could satisfy the full-duplex contract. |
| Capacity/loss/unknown boundary/stale evidence | Report unresolved diagnostic state; never use stale evidence to revoke a newer lease. | User-facing continuation and resource disposition require an explicit contract. |

Failure or natural completion does not prove a candidate was echo. Existing barge rejection is an absence of acceptance, not a negative near-end verdict. Consequently there is no complete safe terminal disposition available from current signals under the full-duplex requirement. A future implementation needs either adequate evidence to resolve the unknown or an explicitly accepted behavior for it. Keeping bytes in quarantine is not a claim that user speech was retained correctly. Neither fallback nor a blocked capture loop is an established solution in this design.

## Minimum decisive experiment: sample 013 replay

Use the existing `suite-206cba2f-gxa1t5yy/runs/completion-population-01/sample-013/runs/completion-20260916T013122Z` capture, its `acoustic/completion/frontend.jsonl`, processed WAV, saved config, and `events.jsonl`. Pin their hashes plus the `206cba2f` source/model/tool versions. No new physical trial, threshold search, transcript-selected crop, or added synthetic silence is needed for this diagnostic.

1. Extend the existing saved-loop diagnostic trace, without changing product policy, to log the exact 1600-sample read interval, neural verdict, gate decision, `track.detected` transition, queued segment state, pause decision, and first permit revocation. Run the production offline loop with a real coordinator/lease and the saved input. Record the pause/start alignment assumptions explicitly: the frontend trace contains callback order/timestamps, but not every live STT read, pause acknowledgement, or per-frame applied gain. Preserve any missing value as an uncertainty rather than claiming an exact live reconstruction.
2. Replay the same saved processed samples through the existing barge policy at the **recorded capture callback boundaries**, using the existing guard, audibility and speaker rules. Map source coverage to the first rendered-reference trace event and observed playback event; retain their difference. Log each qualified decision and compare it with the paused-loop revocation interval. Do not substitute six STT reads for six capture callbacks.
3. Inspect whether the false candidate is a short admitted run that actually ends in the retained trace without a qualified barge request, or a continuing run truncated by cancellation. A completed short run would support the specific diagnosis that paused admission promotes evidence the ordinary policy rejects. If the recording ends first, its later outcome is unknown; do not append silence to manufacture an endpoint. If both policies qualify the same echo, owner unification alone cannot fix this case. If saved replay cannot reproduce the relevant transition under the recorded constraints, report the remaining live-observability gap.

The decisive output is one source-positioned table of candidate onset/end, permit revocation and barge qualification, with explicit timing/gain uncertainty. It tests the reachable mismatch and identifies the candidate interval requiring terminal handling. It cannot establish a negative near-end verdict, exact real-user coverage, or a safe discard rule: sample 013 contains no controlled overlapping user stimulus. Thus it can narrow the design and provide a permanent regression fixture, but cannot by itself resolve unknown full-duplex input or count as a hardware acceptance pass. This experiment is proposed here, not executed by this design update.

## Required regressions and gate

Use production offline/streaming loops with `barrierFrames`, a real coordinator/lease, fake detector and exact PCM assertions. Preserve the existing one-frame thinking-phase revocation test. Add playback-bound cases: one neural/gate-positive frame cannot revoke or seed a resumed transcript without confirmation; valid confirmation promotes the complete source interval once; pre-render frames read late remain exact; boundary-spanning input is not discarded; stale token/generation confirmations do nothing; no-start and canceled-start retain ordinary input. Exercise natural completion, cancellation, capacity exhaustion, and unknown candidate disposition only after that policy is selected. Include sink-write boundary versus queued/event-delayed playback tests.

Then rerun PR194's exact pre-generation physical controls and fresh serial completion echo-only controls. Synthetic checks cannot prove echo rejection. Implementation remains authorized, but source coverage and unknown disposition still need complete safe behavior; passing both retention and echo gates is required for a completion claim.
