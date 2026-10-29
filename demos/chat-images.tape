# Actual Bubble Tea paste, preview, GIF pause/play, admission and thread rendering.
# Provider is deterministic; no microphone, playback or external model requests.
# Run from repo root: just demo-chat-images
Output demos/chat-images.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1100
Set Height 900
Set Padding 14
Set Framerate 12
Set Theme "Dracula"
Hide
Type "env OBEY_VOICE_MEDIA_TERMINAL=record OBEY_VOICE_COLOR_PROFILE=truecolor ./bin/chat-media.test -test.run '^TestChatMediaTerminal$'" Enter
Sleep 500ms
Show
Sleep 4s
Screenshot demos/frames/chat-images-draft.png
Sleep 1s
Sleep 6s
Screenshot demos/frames/chat-images-thread.png
Sleep 5s
