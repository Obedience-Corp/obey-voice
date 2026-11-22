# Actual Bubble Tea picture panel opened with /image: copy a picture, move to
# the other picture, save a copy. The clipboard and viewer are fakes and the
# copy is saved under a temporary home; nothing touches the real desktop.
# Run from repo root: just demo-picture-actions
Output demos/picture-actions.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1100
Set Height 900
Set Padding 14
Set Framerate 12
Set Theme "Dracula"
Hide
Type "env TMPDIR=/tmp OBEY_VOICE_MEDIA_TERMINAL=record OBEY_VOICE_COLOR_PROFILE=truecolor ./bin/chat-media.test -test.run '^TestPictureActionsTerminal$'" Enter
Sleep 500ms
Show
Sleep 3500ms
Screenshot demos/frames/picture-actions-open.png
Sleep 3s
Screenshot demos/frames/picture-actions-copied.png
Sleep 7s
Screenshot demos/frames/picture-actions-save.png
Sleep 1500ms
Screenshot demos/frames/picture-actions-saved.png
Sleep 3s
