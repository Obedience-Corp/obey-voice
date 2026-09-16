# Capture and playback ordering for completion admission

Completion playback can be canceled by paused STT before the ordinary barge-in
controller qualifies an interruption. The saved VW0001 completion sample 013
reproduces that mismatch: paused STT admits samples 99840–101440, while the barge
controller never qualifies before capture ends at sample 101760. The recorded
end truncates the candidate; it does not establish whether later input would
have qualified. This is replay evidence with estimated STT read alignment.

Positioned capture and render observations support one interruption authority
for completion playback. The capture owner now uses the same qualification and
retention rules as ordinary replies; physical acceptance remains a separate gate.

## Capture transport

`Capture.SubscribeFrames` delivers original processed capture callbacks with
the same sample offsets, generation and callback observations as `ReadFrame`.
The ring can combine callbacks into 1600-sample reads; subscribers keep the
original callback cadence. PCM and span slices are shared read-only. Both
subscription APIs use the same `Unsubscribe` lifecycle.

Each subscriber reports its own dropped samples/callbacks on its next delivery.
A dropped device-generation transition stays visible. Ring overflow and resets
remain separate; resetting the ring does not reset subscriber positions or
erase queued callbacks. Loss after the final delivered callback is not reported
without a subsequent delivery.

The existing interruption watcher uses this optional transport when available,
with a PCM-only fallback for legacy monitors. Its request carries the qualifying
callback's spans, unknown-metadata state and accumulated loss/discontinuities.
These positions identify the qualification callback, not the beginning of user
speech. They cannot justify cropping at the neural detector's delayed onset.
Thresholds, arm/tail windows, speaker checks and near-miss behavior are unchanged.

## Playback observation

Completion and scoped ordinary attempts share one `FirstRenderBoundary` across
their segments. The player publishes it inside the valid playback fence, after
the first nonempty device-buffer copy. Queued synthesis, empty callbacks and
revoked writes cannot publish it. Later segments cannot replace it.

Publication does not allocate or wait. Readers acquire an immutable `time.Time`
with its monotonic component. An unpublished boundary is unknown; a reader must
not conclude that capture preceded playback while publication is in progress.
This is a host device-buffer observation, not a hardware DAC timestamp or proof
of audibility. Capture timestamps likewise remain callback estimates. Comparing
them cannot eliminate physical boundary uncertainty.

## Completion capture ownership

Before completion generation, the pipeline binds the original pause token and
playback attempt to the retained STT session. Before any PCM renders, normal STT
admission still revokes the permit immediately. Frames captured before render
keep that behavior even when read later. Missing or boundary-spanning timing is
handled conservatively as ordinary input; callback times are estimates.

Once capture is wholly after the first render and no earlier speech is admitted,
the STT owner parks its next frame and stops draining the existing capture ring.
The microphone, echo processor and ordinary interruption watcher remain active.
The bounded ring supplies the same retained audio as an ordinary response;
there is no second recognizer or unbounded playback archive. Parked audio cannot
seed a partial transcript or independently cancel completion playback.

A qualified ordinary barge request stops the speaker promptly and authorizes
that same STT owner to resume its parked frame and buffered input once. The
qualifying callback is not treated as the speech onset. At response end, recent
audible pause speech uses the existing near-miss retention rule. Otherwise the
owner resets its detector/recognizer state and retains only configured next-listen
pre-roll across the parked frame and unread ring, atomically with capture writes.
That disposition also applies on failure after rendering. A failure before any
render preserves the original listening state. Commands target the original
token and attempt; cleanup cannot affect a replacement session.

Releasing parked input for a qualified interruption or near-miss starts a fresh
listen-timeout window. Generation and playback can outlast the ordinary start
timeout; parked silence must not expire the session before unread speech arrives.
When playback finishes after rendering but before the first frame parks, the
same atomic pre-roll retention applies to the unread ring without a parked frame.

Unqualified playback-period audio is **not accepted by the ordinary barge policy**;
it is not classified as proven echo. This resolves the earlier design's stronger
requirement for a proven user-only interval by matching established product
behavior. The existing arm window, audibility floor, speaker policy and callback
thresholds are unchanged. In particular, this ownership correction does not fix
the separate short-command failure during the arm window. Retained input can
still contain residual echo, and pre-roll can still contain speaker tails.

Actual ring overflow or device discontinuity cannot be reported as complete
retention. Sources unable to provide atomic pre-roll retention defer completion
before generation. Legacy adapters without the optional playback binding keep
their existing capture behavior.

## Verification boundary

Deterministic tests exercise pre-render speech, post-render parking, qualified
retention, normal and near-miss finish, stale commands, cancellation, source loss
and exact pre-roll preservation in both offline and streaming loops. Pipeline
tests cover binding before generation and cleanup after failures. Ring tests
exercise concurrent capture and returned source metadata.

These checks do not substitute for fresh physical completion echo-only,
pre-generation retention and full/short interruption controls, or the required
baseline/worker populations. No acoustic acceptance follows from this design.
