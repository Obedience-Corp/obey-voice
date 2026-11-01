# Prepared native persona bindings

Native-aware CLI, terminal and desktop hosts now install prepared activation,
selection and restoration through the [shared conversation boundary](frontdoor.md).
Generic hosts remain guarded. Replay UI and complete acceptance qualification remain
open. Known answer limitations stay documented for experimental opt-in; general
model accuracy is not a blanket activation predicate.

`nativeaudio.SupportCache.Prepare` reads the explicitly selected runtime version,
model digest and capabilities, then quietly checks required same-model ASR,
audio chat and speech. Transcript off skips ASR. The cache identity includes
endpoint, version, model digest, voice and requirements. A known tuple still
revalidates routes before candidate activation. Successful protocol checks remain
separate from quality qualification. Experimental persona opt-in may retain documented answer
limitations; editor task quality remains a separate contract. Setup uses an embedded synthetic fixture and
software drain; it opens no capture/playback device and installs no assets.

`voiceagent.PrepareNativeCandidate` owns the selected HTTP client, shared body
scheduler, brain and speech resources. Conversation media is borrowed and survives
persona retirement. `NativeCandidate.NewAgent` requires an explicit integration-readiness decision,
rejects prepared config/provider overrides, adopts its owned resources once, and
closes them after joined teardown. Startup selector adoption additionally verifies
prepared config/policies, exact provider identity and the complete bound session/
runtime/conversation scope before persistence. An old candidate cannot be relabeled
for a new epoch with the same session ID. `Options.Input`/`InputFactory` provide a decoder-free constructor
path over the same capture/VAD owner; they exclude Standard recognizer construction.
Ambiguous input choices fail before resource construction.

The selector's opt-in `NativeSelectionHooks` requires explicit candidate/input
preparation and an independent authorization decision. Missing authorization fails
with `ErrNativeUnqualified`. Hosts must provide a live matching conversation scope,
bind media/store/lifetime to that session, and serialize selection with turn/lifecycle
work. The default selector and ordinary binding resolver continue to reject native
activation. The current native-aware host supplies readiness checks for its owned media,
prepared config/protocol and exact live scope. This does not promote model quality
or exact runtime context accounting to qualified.

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
