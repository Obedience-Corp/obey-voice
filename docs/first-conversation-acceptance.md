# First-conversation release acceptance

This procedure separates the reproducible software check from the physical
first-conversation gate. Passing the software check does not certify microphone
permissions, audio quality, model downloads, or installation on a clean machine.

## Automated software check

From the checkout being reviewed, with Go, Python 3, a C compiler and `just`:

```sh
just desktop test first-conversation
```

To retain evidence at a chosen location, supply a **new** directory:

```sh
just desktop test first-conversation --output /tmp/obey-first-conversation-review
```

The recipe builds the real companion with the existing `desktopfixture` tag.
That tag requires an explicit temporary config directory and never uses the
normal installation root. The test starts without `config.yaml` or setup
progress, saves configuration through the real CLI, checks a local Ollama-shaped
test server, completes a text-only silent conversation, reads its saved session,
and restarts the process to verify setup progress and transcript resume.

Missing-model and provider HTTP 503 checks must return actionable errors, and
checking again after recovery must succeed. The speech-model cache starts empty
and must remain empty. Responses come from a deterministic local HTTP server;
no inference, microphone, speaker or model download is involved. The fixture
disables tools, workers and skills. Go dependencies may be downloaded during
the source build; existing compiler/dependency caches are allowed.

Evidence includes `result.json`, protocol JSONL for fresh/configured/restarted
processes, stderr, the disposable configuration, setup progress and session
file. A nonzero exit is a failed check; inspect the retained evidence. Do not
publish raw logs from a real-user run without reviewing them. These fixtures
contain synthetic conversation content.

## Native-window checks

Existing macOS fixtures cover the window interactions separately:

```sh
just desktop test setup
just desktop test setup-replay
```

Run one at a time and open the printed test app. In `setup`, configure the
printed local provider URL and `desktop-test` model; choose text-only and silent
mode, complete setup, send a message, end the conversation and reopen it from
history. Quit/relaunch the same test app while the fixture server is running and
check that setup completion remains saved. Do not select microphone or playback
in this software check. `setup-replay` uses simulated protocol events to check
download failure, cancel/retry and permission-error presentation. It does not
prove real downloads or OS permission recovery. Stop fixtures with Ctrl+C.

## Physical release gate

Use a disposable OS account or clean test machine with no Obey configuration or
model cache. Do not rename or delete the owner's existing configuration. Start
with macOS arm64, the native desktop, local Ollama conversation, and the default
supported local speech stack. Record the actual selected model IDs, versions,
hardware, operating system and available storage; this document does not claim
that every provider/platform combination is supported by that observation.

1. Clone the exact candidate commit and follow [Install](install.md), including
   all source-build prerequisites. Record commands, tool versions, failures and
   the installed binary identity. An existing checkout build is not a substitute
   for this clean-install step.
2. Launch with no saved configuration. Confirm the setup guide appears. With
   Ollama stopped, verify the provider error is actionable. Start it, select a
   missing model to verify that error, then install/select the intended model
   and check recovery.
3. Download speech assets into the initially empty cache. Cancel one download,
   retry, and record whether partial data is recovered safely. Repeat with a
   real interrupted connection where practical. Confirm readiness reflects the
   complete assets and no existing personal cache was consulted.
4. Deny microphone permission once. Confirm the app explains the failure and
   permits a text-only conversation. Grant permission through macOS Settings,
   relaunch if requested, and verify that normal capture recovers.
5. Start a voice conversation. Speak a synthetic test sentence, hear a complete
   intelligible response, then interrupt a longer response. Verify capture
   resumes and the next utterance receives one response without stale playback.
   If distortion occurs, retain scoped diagnostics and link the playback-recovery
   issue; a successful text fixture does not waive the failure.
6. End the conversation, verify it in history, quit the app, relaunch and resume
   the saved conversation. Check that settings and setup completion persist.

Record pass/fail and evidence for each step against the exact candidate. Keep
untested OS/device/provider combinations explicit. The gate remains open while
real download, microphone, playback or restart evidence is missing. An owner
publication decision follows these checks and the other release gates.

## Initial software evidence

The first run on 2026-11-03 passed against engine baseline `9d7fc4fb` with this
harness added: fresh setup, real CLI writes, missing-model/provider recovery,
reply persistence, process restart and resume. This was a macOS developer host
with existing build dependencies. No native-window or physical gate result is
claimed by that run.
