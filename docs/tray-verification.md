# Menu bar and tray verification

Design: campaign work item WI-2f62ba. Branch: feat/menu-bar-tray.

## Automated evidence

- `just check`: 22 macOS Rust tests, isolated Go race tests, Clippy, formatting,
  vet, file-size gate, and debug build pass.
- `just test linux`: Linux ARM64 build and 21 tests pass in Docker. The real
  D-Bus integration test runs on a private session bus and verifies a watcher
  without a panel, a panel appearing, disappearing, returning, and watcher loss.
- Reopen callback tests verify that the existing delegate class is preserved and
  that an existing reopen implementation is never overwritten.
- Menu policy tests cover conflicting actions and preserving recording status
  when an unrelated error occurs. Close policy tests cover unavailable/disabled
  tray and explicit Quit.
- `just bundle`: strict signature verification passes for the local macOS bundle.

## Native macOS replay

Used `just test events` with the protocol-only engine; no personal voice config
or audio devices. Verified through native accessibility UI and fixture files:

1. Close with the preference enabled hides the window; app and engine stay alive.
2. Reopening the same app restores its window through the native reopen callback.
3. Turning the preference off and closing exits. `desktop-ui.ron` contains
   `keep_in_tray: false`, and relaunch displays the unchecked preference.
4. Re-enabling the preference creates a new status item. A replay meeting starts,
   pauses, hides/reopens with its transcript intact, resumes, and stops to review.
   These use the same meeting actions as the tray menu.
5. The replay provider failure remains visible in the app.
6. Command-Q exits even when hidden. Replay action logs contain `shutdown` and
   the owned app/engine processes are gone afterward.

## Remaining manual acceptance

Computer-use could operate the application window but did not expose the native
status item/context menu. Direct icon click/right-click, visual icon contrast,
and each menu entry still need a manual macOS pass. No Linux graphical desktop
was available: D-Bus integration and compilation are verified, not panel rendering
or Wayland window focus. Live microphone/meeting capture remains an opt-in
hardware check. The protocol fixture does not prove real recording persistence.

For a manual pass, run `just test events`, open the printed app, and use its menu
bar icon. Check idle/error/recording/paused labels, open while hidden/minimized,
new conversation/meeting setup, pause/resume, stop/review, diagnostics, and Quit.
On Linux, repeat with a real StatusNotifierItem panel and temporarily remove its
host while Obey is hidden; the window should return. Desktop settings must remain
reachable and normal close must quit when no tray is available.
