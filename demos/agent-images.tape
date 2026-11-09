# Actual Bubble Tea image input and worker image/GIF replies.
# Provider is deterministic; no microphone, playback or external model requests.
# Run from repo root: just demo-agent-images
Output demos/agent-images.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1100
Set Height 900
Set Padding 14
Set Framerate 12
Set Theme "Dracula"
Hide
Type "env OBEY_VOICE_MEDIA_TERMINAL=record OBEY_VOICE_COLOR_PROFILE=truecolor ./bin/chat-media.test -test.run '^TestAgentMediaTerminal$'" Enter
Sleep 500ms
Show
Sleep 4s
Screenshot demos/frames/agent-images-draft.png
Sleep 1s
Sleep 8s
Screenshot demos/frames/agent-images-thread.png
Sleep 2s
Screenshot demos/frames/agent-images-paused.png
Sleep 2s
Screenshot demos/frames/agent-images-resumed.png
