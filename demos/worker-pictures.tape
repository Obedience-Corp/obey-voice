# A task's picture in the worker view, at the size the chat gives it.
# The recording terminal has no Kitty graphics, so this shows the half-block
# fallback; Ghostty and kitty draw the same box at full resolution.
# Run from repo root: just demo-worker-pictures
Output demos/worker-pictures.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1100
Set Height 900
Set Padding 14
Set Framerate 12
Set Theme "Dracula"
Hide
Type "env OBEY_VOICE_WORKER_PICTURE_TERMINAL=record OBEY_VOICE_COLOR_PROFILE=truecolor ./bin/chat-media.test -test.run '^TestWorkerPicturePlaceholdersReachTheTerminal$'" Enter
Sleep 500ms
Show
Sleep 7s
