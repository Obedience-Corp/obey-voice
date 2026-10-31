# Editor-owned finite runtime evidence

WI-43b072 · 2026-10-30 · Apple Silicon macOS. This is a finite job adapter
checkpoint; editor generation UI and independent speech quality remain open.

The actual pinned llama.cpp b11960 runtime loaded the installed model/projector,
generated narration from the explicit script, wrote the artifact, and exited.
No microphone, speaker output, persona, downloads or shared Ollama changes were
used. The request retained the existing 30-second source reference and its
130-second source origin. Narration speaks the supplied text; it does not
transform that recording or prove long-recording understanding.

[The receipt](managed-runtime.json) records the exact worker, runtime, model,
projector and WAV digests, sampling settings, source coverage and process checks.
[The sandbox profile](loopback-only.sb) denied external networking while allowing
loopback for the owned server. A separate socket control connected to loopback
and received EPERM connecting to `1.1.1.1:443` under the same profile.

- Completed job: 111,360 samples at 24 kHz, 4.64 seconds; 1.92 seconds elapsed
  including startup, hashing and publication. WAV geometry and SHA-256 match.
- SIGTERM after acceptance: cancelled terminal, exit 1, 0.24 seconds to cleanup;
  no output artifact, runtime, lock or staging directory survived.
- Forced worker **group** kill after acceptance: exit -9, 0.32 seconds, no owned
  process survived. No completed artifact existed. As expected after SIGKILL,
  incomplete directories and lock remained until the owning harness retired them
  after reaping the group. The editor must retain this caller cleanup behavior.
- The source hash was unchanged. Normal completion and cancellation removed their
  own scratch. The shared Ollama service was not part of either process group.

The first harness attempt completed all worker actions but failed in its own
cleanup check when an already-dead process group disappeared between inspection
and kill. The corrected harness checks that children exit before fallback cleanup
and tolerates only that lookup race. The second attempt is the recorded pass;
no runtime survivor was hidden by the cleanup path.

The reusable [probe](../probe/managed_runtime.py) requires explicit worker,
config, request and a new output directory; it never opens an audio device.
Run it with `just test managed-audio-proof WORKER CONFIG REQUEST NEW_OUTPUT PROFILE`.
Fixtures cover altered assets, conflicting runtime selection, startup failure,
readiness timeout, inherited environment isolation, malformed sample formats,
non-finite samples, truncated HTTP bodies, cancellation and atomic publication.

These are transport, ownership and offline-inference checks. No listener judged
intelligibility, voice identity, pronunciation, preservation or timing. The
1024-byte narration limit and recorded acoustic-frame ceiling are explicit;
conversion, dubbing, exact word timing, long script chunking and clean signed
packaged distribution remain unqualified. The editor is not yet wired to this
new worker revision, so its Generate action remains disabled.
