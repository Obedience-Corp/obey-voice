# @path completion in the real chat composer, routed through the real persona
# Selector and Pipeline in a disposable campaign under the test's temp dir.
# Only the brain is a fixture; no microphone, playback or model requests.
# Run from repo root: just demo-chat-path-completion
Output demos/chat-path-completion.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1100
Set Height 820
Set Padding 14
Set Framerate 12
Set Theme "Dracula"
Hide
Type "env OBEY_VOICE_PATH_TERMINAL=record OBEY_VOICE_COLOR_PROFILE=truecolor ./bin/chat-path-completion.test -test.run '^TestPathMentionsTerminal$' 2>/dev/null" Enter
Sleep 500ms
Show
Sleep 17s
