# Actual Bubble Tea worker reply whose picture references end its line.
# Provider is deterministic; no microphone, playback or external model requests.
# Run from repo root: just demo-agent-images-end-of-line
Output demos/agent-images-end-of-line.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1100
Set Height 900
Set Padding 14
Set Framerate 12
Set Theme "Dracula"
Hide
Type "env OBEY_VOICE_MEDIA_TERMINAL=record OBEY_VOICE_COLOR_PROFILE=truecolor ./bin/chat-media.test -test.run '^TestAgentMediaTerminalEndOfLine$'" Enter
Sleep 500ms
Show
Sleep 16s
