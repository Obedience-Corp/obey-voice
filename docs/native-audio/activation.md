# Prepared native persona bindings

Production activation remains guarded. The activation worktree adds candidate
setup and transaction machinery; it does not yet connect persona controls,
startup/restart/replay, or qualify A20 context behavior and model quality.

`nativeaudio.SupportCache.Prepare` reads the explicitly selected runtime version,
model digest and capabilities, then quietly checks required same-model ASR,
audio chat and speech. Transcript off skips ASR. The cache identity includes
endpoint, version, model digest, voice and requirements. A known tuple still
revalidates routes before candidate activation. Successful protocol checks remain
separate from quality qualification. Setup uses an embedded synthetic fixture and
software drain; it opens no capture/playback device and installs no assets.

`voiceagent.PrepareNativeCandidate` owns the selected HTTP client, shared body
scheduler, brain and speech resources. Conversation media is borrowed and survives
persona retirement. `Options.Input`/`InputFactory` provide a decoder-free constructor
path over the same capture/VAD owner; they exclude Standard recognizer construction.
Ambiguous input choices fail before resource construction.

The selector's opt-in `NativeSelectionHooks` requires explicit candidate/input
preparation and an independent authorization decision. Missing authorization fails
with `ErrNativeUnqualified`. Hosts must provide a live matching conversation scope,
bind media/store/lifetime to that session, and serialize selection with turn/lifecycle
work. The default selector and ordinary binding resolver continue to reject native
activation. No production authorization override is installed by this checkpoint.

At the serialized selection boundary:

1. Quiesce optional captions without retiring the old client, and close capture
   admission. Cancel/join active, yielded and in-flight startup readers.
2. Prepare candidate routes/brain/voice/input and transfer canonical continuity.
   Standard projection requires actual ready transcripts or an explicit working
   summary. Typed accompaniment cannot replace a missing recording.
3. Validate the input lease and candidate lifetime. Acquire the caption publication
   writer fence, then persist before publishing input/policy/brain/voice/identity.
4. On failure, discard candidate resources and reopen the unchanged old binding.
   On durable success, transfer brain ownership to normal pipeline retirement,
   publish the new binding token, release the writer, then retire old resources.

A reader teardown timeout keeps capture closed until successful eventual teardown.
A non-nil Close error latches an actionable failure: restart voice mode before
recording again. Retry does not start a second reader. Host serialization must span
Validate, persistence and publication; the lease is not an alternative dispatcher.

Caption publication holds a read lease while updating the original utterance and
autosaving. It checks context, binding token, session and conversation epoch; the
canonical updater also checks utterance/capture identity. Producers are joined before
taking the writer fence. Clear/Close join optional captions before mutating history.
A recording captured before a mode/policy replacement fails explicit admission and
requires a fresh recording; it cannot inherit new spoken controls after capture.
Native policies participate in binding identity, while inactive native drafts do not
reload a Standard response.

Verification includes deterministic HTTP setup and candidate lifetime fixtures,
reader startup/cancel/late-close ownership tests, atomic publish/rollback and
qualification/projection negatives, retired caption publisher rejection, and actual
HTTP caption teardown before Clear/Close. Combined persona, constructor, pipeline
and nativeaudio race suites pass; changed-code lint reports zero issues. These
fixtures do not qualify native UI activation, listener quality, long editor tasks,
exact audio token accounting, or server context enforcement.
