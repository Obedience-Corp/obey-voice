# Browse sessions — view a saved conversation read-only, then delete one.
#
# Fixture sessions in a disposable install; no session is opened, so no
# model, microphone, or talk key starts.
# From repo root after `just build`:
#   just demo-sessions-browse

Output demos/sessions-browse.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1040
Set Height 720
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 40ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "rm -rf -- /tmp/obey-voice-sessions-demo" Enter
Type "python3 demos/fixtures/sessions-browse.py /tmp/obey-voice-sessions-demo" Enter
Type "export OBEY_VOICE_DEMO_ROOT=/tmp/obey-voice-sessions-demo" Enter
Type "clear" Enter

Type "./bin/demo-terminal.test -test.run '^TestDemoTerminalHarness$'"
Sleep 400ms
Enter
Sleep 5s
Show
Sleep 1s

# Launcher: Continue, New conversation, Browse sessions.
Down
Sleep 250ms
Down
Sleep 500ms
Enter
Sleep 1500ms
Screenshot demos/frames/sessions-browse-list.png
Sleep 500ms

# View the first conversation without opening it.
Type "v"
Sleep 2s
Screenshot demos/frames/sessions-browse-view.png
Sleep 500ms
Down
Sleep 300ms
Down
Sleep 300ms
Down
Sleep 1s
Escape
Sleep 1s

# Delete the oldest conversation: d arms, y confirms.
Type "G"
Sleep 800ms
Type "d"
Sleep 1800ms
Screenshot demos/frames/sessions-browse-confirm.png
Sleep 500ms
Type "y"
Sleep 2s
Screenshot demos/frames/sessions-browse-deleted.png
Sleep 500ms

Hide
Escape
Sleep 300ms
Type "q"
Sleep 300ms
Type "rm -rf -- /tmp/obey-voice-sessions-demo" Enter
