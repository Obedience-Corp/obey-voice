# Playback diagnosis and recovery

In an active desktop conversation, **Reset audio output** interrupts the current
spoken reply and releases the output device. The next spoken reply opens a fresh
stream. The session, transcript, persona, providers, microphone preference, and
volume remain in place. The reset confirmation means teardown completed; a later
open failure is reported separately and can be retried after checking the device.

Unexpected native stops, missing callbacks for three seconds, and output-route
changes trigger recovery on a worker outside the audio callback. Routes are
checked every five seconds. Automatic recovery gets three attempts with bounded
backoff; repeated unstable opens share that budget. Thirty seconds of healthy
operation or an explicit reset replenishes it. An explicitly selected missing
device is reported unavailable. Follow-default output resolves the current
default again. Canceled speech is never replayed automatically.

## Capturing an affected turn

1. Choose **Capture audio diagnostics** before an affected reply. The UI shows
   capture activation and the local directory. Capture stops after 60 seconds,
   at its 32 MiB budget, when stopped explicitly, or when output is reset.
2. Preserve the affected capture, then choose **Reset audio output**.
3. Start another capture and request a comparable reply in the same conversation.
   Record whether the audible issue cleared and whether it sounded like distorted
   speech, overlapping speech, or microphone self-triggering.

The existing recorder writes source WAVs, rendered callback PCM, callback timing,
and capture/drop metadata. The diagnostic budget includes conservative overhead;
its byte count is not a measurement of audible output. Three capture directories
are retained. Expired captures are removed after 24 hours while the process is
running or when the next capture prunes the directory. Copy evidence elsewhere
if it must be retained longer. Diagnostic audio is opt-in; routine lifecycle logs
contain formats, identity, generation, gain, gaps, underruns, and recovery results,
not speech content. Callback capture never performs file I/O.

Clean source PCM and clean callback PCM do not establish clean physical output.
Client format is not the physical device clock. Unknown backend properties stay
unknown. Physical listening or a separately captured output is needed to isolate
backend/device distortion. The watchdog cannot detect distortion while callbacks
continue normally.

## Reset boundaries

Output generations fence attempts before synthesis, including later retries and
fallbacks. Reset cancels queued and pumping segments, joins the old reference
ingress after native callbacks stop, and clears cached format, observed period,
resampler, and queued echo-reference samples. Frontend ownership is retained.
The optional APM frontend clears its partial render frame; its installed native
shim does not expose an independent internal reverse-history reset, so learned
native AEC state is not recreated. Physical AEC/interruption acceptance is still
required for that frontend.

## Verification

- `just test audio-crackle` runs software corruption/layout regressions.
- Playback and pipeline race suites cover reset/recovery, cancellation, delayed
  synthesis, retry limits, and reference state.
- `cd apps/desktop && just check` runs desktop lint, tests, and build checks.
- `cd apps/desktop && just test audio-output` opens an isolated UI fixture with
  the real controller and player reset/capture APIs. It uses a silent local test
  conversation and never submits PCM or opens audio devices. Verify history before
  and after reset, continued replies, capture visibility, and persisted session.
- `just test audio-hardware` is opt-in and plays a short tone. Coordinate it with
  active sessions. It is separate from an affected-turn before/after experiment.

The iFi output still requires affected-turn listening/capture, sustained reuse,
idle/resume, default-route changes, selected-device disappearance/reconnection,
and ordinary speech/interruption checks. The existing rate order is unchanged;
any rate-policy change needs device evidence and Studio Display regressions.
