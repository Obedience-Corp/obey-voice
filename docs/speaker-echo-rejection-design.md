# Speaker echo rejection and retained interruption

Status: proposed design, before runtime implementation. Workitem: WI-fd6296.
Owning acceptance task: VW0001, phase 003, acceptance/release task 02.
Source reviewed: `4b715934`, refreshed onto `9b9439f7` before delivery.
The later TUI changes leave the reviewed audio/STT/pipeline paths unchanged;
merged PR184 is `9f278df0`.

When a background result is spoken through local speakers, Obey Voice must not
turn its own output into a user turn or cancel its delivery. A real user must
still be able to speak over the response, including its first words, and have
their complete utterance retained once. This proposal defines the ownership
contract, evidence needed to choose the signal-processing correction, and the
implementation gates. It does not claim that echo rejection is implemented.

## Existing behavior and confirmed failures

The player already forwards its rendered PCM through
[`aec_ref.go`](../pkg/voiceagent/audio/playback/aec_ref.go) to the shared audio
frontend. The frontend processes microphone audio before VAD and transcription.
Both the local NLMS path and the optional WebRTC/APM path exist. The follow-up
extends these paths; it does not introduce another TTS provider or remote API.

[`completion_capture.go`](../pkg/voiceagent/pipeline/completion_capture.go)
retains the same STT session while capture yields for a completion.
[`offline_loop.go`](../pkg/voiceagent/stt/offline_loop.go) and
[`streaming_loop.go`](../pkg/voiceagent/stt/streaming_loop.go) keep consuming
frames. Speech admission updates utterance state before
[`pause.go`](../pkg/voiceagent/stt/pause.go) checks whether to revoke the capture
permit. Revocation cancels the completion through
[`completion_lease.go`](../pkg/voiceagent/pipeline/completion_lease.go).

Separately, [`interrupt.go`](../pkg/voiceagent/pipeline/interrupt.go) evaluates
barge-in with a once-per-turn 600ms startup guard, neural speech, audibility and
the configured speaker policy. Paused STT does not receive that playback phase.
These independent decisions can disagree about the same captured audio.

| Retained observation | Conclusion and limit |
|---|---|
| c27ae4b6 NLMS completion 211709 cancels before the external stimulus opens; raw and processed cancellation-window text matches rendered output. | Strong evidence of playback echo reaching the barge detector. |
| c27ae4b6 APM completion 211757 cancels without a barge event; replay admits startup echo through paused STT. | A second cancellation route. Estimated admission 0.49s after playback is approximate, because exact capture/playback ordering was not recorded. |
| 867750b7 interruption 215636 retains the question with an assistant-response prefix. | Capture retention can include own speech even when interruption succeeds. |
| a62ba9f3/2a2e5570 trimming at first neural confirmation minus 300ms passes software tests but loses the opening question physically. | Neural confirmation is not the utterance's beginning. Both commits were reverted in e2cd0919. |
| bf9e30d9 baseline 222152 contains unrelated speech in the full microphone recording; clean fixture decoding is exact. | That sample cannot isolate segmentation loss. Its contamination is separate from the confirmed NLMS echo failure. |

The acoustic harness gives the response player a reference frontend and gives
the external stimulus player none. It does not feed the stimulus into the echo
canceller by mistake. Both signals currently use the same physical speaker;
this tests acoustic overlap, not spatial separation or human speaker identity.
Raw recordings remain outside git. The campaign's VW0001 audio acceptance and
completion-capture-arbitration records retain exact run paths and fingerprints.

## Proposed ownership contract

### 1. Establish sample ordering before changing speech decisions

Extend the existing capture and playback seams with monotonic sample spans,
capture generation and actual first-PCM delivery boundaries. Preserve positions
through buffering and identify gaps or overflow explicitly. A delayed reader
must not classify thinking-phase speech as playback echo. Frames crossing a
playback boundary must be split or represented as explicit subspans.

`audio.Frame.StartedAt` exists, but `Capture.ReadFrame` currently leaves it unset.
Stamping only the read time would not solve this. Playback callbacks must stay
nonblocking: publish ordering metadata through the existing bounded handoff,
and do model work outside callbacks. A dropped reference or timing event is
unknown input, not evidence that the user was silent.

These positions are for ordering and preservation. They must not reintroduce
the rejected first-neural-confirmation crop.

### 2. Correct the existing AEC using paired evidence

Replay ordered raw-capture/render-reference traces to check reference delay,
resampling, gaps, adaptation and residual echo. Compare assistant-only and
overlapping-user recordings using the same candidate and source/configuration.
An improvement on echo-only input is insufficient if it removes user speech.
Use the existing [AEC probe](aec-probe.md) as a signal diagnostic, not the
completion acceptance oracle. Its aggregate energy-reduction figure cannot
prove that the retained signal is the user.

The concrete AEC correction is not selected yet. Prior leakage adjustments
failed paired trace evaluation and were not applied. The next experiment must
name a defect in the captured reference/capture relationship, a predicted
change, and a positive/negative test before running. Keep the configured
frontend choice until comparative evidence justifies a separately reviewed
change; merely selecting WebRTC does not fix the observed APM cancellation.

### 3. Make the capture owner decide admission for yielded completion

Bind playback lifecycle to the existing pause token and completion attempt
before playback starts. Use the existing capture generation, scope and lease
identity; avoid a second independent epoch authority. Thinking, startup,
playing, draining and finished must be distinguishable for this attempt.
Late events from a previous attempt cannot affect the replacement.

During playback, retain uncertain input separately from committed utterance
state. It must not update the speech tracker, recognizer context or captured-
input flag before admission. Preserve onset context and sample spans so a later
valid admission can transfer the complete utterance once. The same STT frame
owner owns promotion and its detector; another goroutine must not reset or trim
that live state.

For a yielded completion, route interruption evidence to that owner. A valid
user admission commits retained input, revokes the existing permit and lets the
existing lease cancellation stop playback. The barge watcher cannot separately
choose which samples the STT owner retains. Thinking-phase speech keeps ordinary
immediate admission. Existing mute and speaker policy still apply.

Own echo must not become user input merely because playback ends, a timer
expires or capture resumes. Resume transfers committed user input once and
retires only the matching attempt. No captured candidate may be silently lost
when playback fails, the session is replaced, or a bounded queue overflows.
Overflow must be observable and block a successful acceptance result.

## Decisions still required before admission implementation

This is a reviewable proposal, not an implementation-ready speech classifier.
Resolve these decisions with recorded evidence before wiring candidate input
into production:

- **Admission evidence:** choose the per-frame or bounded-window evidence that
  distinguishes residual echo from overlapping user speech. Frontend output is
  currently just processed PCM. The roughly once-per-second `FrontendStats`
  are diagnostic only. Reference correlation proves echo is present, not that
  the user is absent; neither correlation nor a linear residual alone permits
  discarding a frame.
- **Unknown input and memory bounds:** specify candidate storage capacity,
  resolution deadline, overflow behavior, and resume behavior. Bound these
  using the existing utterance/session limits and measured latency requirements.
  An unresolved candidate cannot be silently classified as echo, automatically
  promoted at guard expiry, or kept forever. The selected behavior must pass both
  echo-only and overlapping-user controls before release.
- **Early short speech:** the desired result is to retain and respond to a short
  interruption wholly inside startup, as well as a longer phrase spanning it.
  The current 600ms guard is an implementation constraint to investigate, not
  proof that early speech can safely be ignored. Any change to that policy needs
  explicit design rationale and regression evidence; longer guards are not an
  accepted remedy for the current defect.

## Acceptance matrix

| Case | Required observable result |
|---|---|
| Assistant-only response, including its first segment and pauses | No user transcript or self-interruption; exactly one audible communicated receipt for a successful completion. |
| User begins during thinking; capture reader is delayed past playback start | Immediate ordinary admission when processed; full input retained once; completion attempt canceled. |
| User begins with playback, during startup, or after arming | Production interruption stops playback; full known phrase including opening words is retained once. |
| Short stop/wait entirely inside startup | Preserved and delivered as real input; cannot be waived because it fits inside the guard. |
| Real speech overlaps strong echo | Correct user transcript, no assistant prefix, no missing syllables; signal suppression alone cannot pass. |
| Playback never starts, fails, is muted, or drains between segments | No phantom user turn, no false delivery receipt, no lost committed input. |
| Resume, stale callback, replacement, cancellation and repeated completion | One transfer of retained PCM; old tokens cannot revoke a new permit; callbacks and readers are joined safely. |
| Delayed/dropped reference, capture overflow or competing ambient speech | Explicit invalid/degraded evidence; preserve failure artifacts and never count it as a clean acceptance sample. |

Deterministic tests must exercise both production STT loops plus the real
pause/lease/playback ownership path, with barriers and exact PCM assertions.
Include delayed readers and realistic delayed neural confirmation; a fake that
declares the first sample to be speech cannot prove onset preservation.

Then run automated physical microphone/speaker controls with pinned binaries,
models, configuration, device identities and negotiated rates. Record raw,
processed and rendered audio plus timing and final persisted delivery state.
Use varied fixtures and declared volume/device conditions; do not change a live
session or global audio settings implicitly. Human listening is not a gate.
Report detector-to-stop and stimulus-to-stop separately, including waiting
before admission. VW0001's external campaign acceptance criterion is “no
regression >50 ms against baseline; absolute result reported,” recorded in the
external My_Tools campaign's
[execution contract, “Barge-in” row](https://github.com/lancekrogers/TOOLS_PLANNING_REPO/blob/370362d536224373057c1ef109a4dcf4156957b6/workflow/design/.dungeon/completed/2026-09-10/obey-voice-background-work-contract/EXECUTION-CONTRACT.md#L106).
That immutable source is in a separate repository; its actual archive directory
is `.dungeon`. Older `dungeon` references in this repository describe historical
campaign layouts, not the location of this contract. This is a campaign release
criterion, not a threshold currently enforced by the repository's convoflow
harness. The implementation must record comparable baseline/candidate timing
populations and explicitly evaluate that criterion in the acceptance task.

Preserve failed samples and classify contaminated recordings separately. First
pass full-phrase completion/interruption controls, then collect fresh 3-warmup +
30-measured baseline and worker populations with independent endpoints. Keep
the existing exact-transcript and audible-delivery assertions. Do not pool the
full and latency-brief fixture profiles or replace failed samples.

## Implementation and delivery sequence

1. Add ordering/overflow evidence and regressions at the existing capture and
   reference handoffs, without changing speech admission. Use saved traces first.
2. Identify and verify a bounded AEC correction on paired echo/user cases. Stop
   a candidate that worsens user preservation or fails its predicted correction.
3. Resolve the three admission decisions above, then implement attempt binding
   and capture-owner promotion in both STT loops with race-tested lifetime cases.
4. Pass physical controls, collect the valid latency pair, and complete VW0001's
   remaining compatibility, review and release gates. Keep scoped implementation
   PRs separate from this design proposal and link their actual evidence.

No additional model dependency, paid service, default-provider switch or
installed-binary change is part of this proposal. Each runtime change must have
a reversible commit and preserve the existing work receipts and session state.
This design PR itself does not complete VW0001's audio task.
