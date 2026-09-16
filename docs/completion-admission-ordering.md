# Capture and playback ordering for completion admission

Completion playback can be canceled by paused STT before the ordinary barge-in
controller qualifies an interruption. The saved VW0001 completion sample 013
reproduces that mismatch: paused STT admits samples 99840–101440, while the barge
controller never qualifies before capture ends at sample 101760. The recorded
end truncates the candidate; it does not establish whether later input would
have qualified. This is replay evidence with estimated STT read alignment.

This change supplies the missing ordering information for a shared admission
decision. It does not yet change that decision or repair the acoustic failures.

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

## Remaining admission work

The STT owner still needs token-scoped playback binding and confirmation before
provisional playback-period audio may revoke a completion lease. Pre-render input
must retain its ordinary admission and exact PCM. Candidate retention, source
coverage, loss and terminal handling must preserve real speech without promoting
assistant echo. The new metadata does not classify unknown audio for that owner.

Regression coverage exercises actual ring ingestion and device callbacks,
subscriber loss/generation changes, concurrent publication/unsubscription,
unchanged callback cadence, and fenced/empty/later-segment render behavior.
Those tests do not substitute for the open physical startup, completion and
baseline/worker interruption acceptance populations.
