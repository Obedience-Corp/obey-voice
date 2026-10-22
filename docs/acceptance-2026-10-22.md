# Desktop acceptance — 2026-10-22

Campaign work item: WI-dd2bbb (`workflow/verification/obey-voice-agent-acceptance`).
Baseline: merged PR #8, `c0e01a902c1c220dbb4d005e3fb79f2f76263213`.

## Confirmed defect and fix

An engine capture failure or spoken stop phrase emits `meeting_stop_requested`.
The app previously sent a stop without marking the meeting as saving. Screen
and tray controls stayed enabled; another stop could return “No meeting is
recording” after the first save succeeded. A stop during startup also let the
late start reply restore recording controls.

All stop sources now share one guarded save path. Pending saves disable controls,
late startup replies preserve the saving state, and duplicate/late stop events
are ignored. Engine loss clears outstanding meeting controls so a new engine
can save another meeting. The regression failed against the baseline and passes
with this change, including startup ordering and reconnect bookkeeping.

## Evidence

| Check | Result and boundary |
| --- | --- |
| `just check` | Passed: 24 macOS Rust tests, Go race suite, Clippy, formatting, Go vet, build, 500-line gate. |
| `just test linux` | Passed: 23 Linux ARM64 tests in a private D-Bus session. Exported menu labels/enabled states, activation, Diagnostics, Pause, Stop, and host loss/recovery tested through D-Bus. No graphical panel exercised. |
| `just bundle` | Passed strict macOS bundle signature verification; not installed. |
| `just test routing` | Real engine adapter delivered a durable note/transcript to a disposable campaign in its own cwd. Importer failure with process exit 0 returned a failed receipt and retained the local bundle. |
| Native fresh setup | Empty temporary config → missing-model guidance → saved local provider/model → text-only mode requiring no speech assets → real engine test reply → Finish → Home. Config, completion JSON, and conversation text persisted; relaunch returned to Home with the session visible. |
| Native meeting replay | Partial/final transcript displayed; failed note retained its draft; retry displayed the saved note; pause reset the meter; stop opened review. Failed routing displayed the error and “Meeting notes kept locally.” Replay is synthetic capture, not an audio or durable meeting writer test. |
| Native setup replay | Missing assets, download interruption, cancellation, and successful retry displayed correctly. Failed provider greeting showed recovery instructions; retry succeeded. Finish stayed disabled without audio confirmations. No real download, authentication, or audio hardware used. |
| Native diagnostics | Dialog exposed log path, filters, log content, and copy controls. Persistent JSONL recorded engine/tray/window events. |
| Native lifecycle | Close logged `window.hide`; reopening dispatched Open to the same running app. Command-Q exited the app and owned engine. Completed setup survived quit/relaunch. |

Native flows above used the merged baseline (setup replay used the first fix
build). Final source changes are covered by the regression and full checks.
All fixtures used disposable config/data and were stopped after testing. No
personal config, real provider account, microphone, or speaker was used.

## Remaining acceptance

This is not hardware or desktop-environment certification. The following remain
unverified and must be tested on the relevant machine before release:

- Actual macOS menu-bar icon appearance and direct menu clicks. The available
  automation surface exposes the app window/menu, not its native status item.
- Linux graphical panel behavior and appearance (for example KDE and a GNOME
  StatusNotifier/AppIndicator extension). Only the actual D-Bus adapter was tested.
- Real microphone permission denial/recovery, device loss, speech transcription,
  spoken playback, and recording while hidden/minimized.

Next executable check: run `just ui-fixture`, keep its temporary config isolated,
and manually exercise native menu actions and opt-in audio on macOS; repeat the
tray interaction on a Linux graphical host. Do not infer these outcomes from the
protocol replay or passing compilation.
