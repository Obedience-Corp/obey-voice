# Workers manager — a job whose tools keep returning the same output.
#
# OBEY_VOICE_DEMO_WORKERS=same_output shows one fixture job, no model: its
# worker rewords a listing command each second and gets the same listing
# back, so its line reads "same output for N rounds" and N rises. Nothing
# stops the job. From repo root after `just build`:
#   just demo-workers-same-output

Output demos/workers-same-output.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1040
Set Height 640
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 40ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi OBEY_VOICE_DEMO_WORKERS=same_output" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export OBEY_VOICE_DEMO_ROOT=$(mktemp -d /tmp/obey-voice-demo.XXXXXX)" Enter
Type "mkdir -p $OBEY_VOICE_DEMO_ROOT/sessions" Enter
Type "cat > $OBEY_VOICE_DEMO_ROOT/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: local-model" Enter
Type "tts_provider: kokoro" Enter
Type "models_dir: /tmp/obey-voice-demo-models" Enter
Type "voice_workers_disabled: false" Enter
Type "EOF" Enter
Type "clear" Enter

Type "./bin/demo-terminal.test -test.run '^TestDemoTerminalHarness$'"
Sleep 400ms
Enter
Sleep 5s
Show
Sleep 1s

# Launcher: New conversation, Personas, Workers (no sessions).
Down
Sleep 250ms
Down
Sleep 400ms
Enter

# The count rises once a second from three.
Sleep 5s
Down
Sleep 200ms
Down
Sleep 1s
Screenshot demos/frames/workers-same-output.png
Sleep 1s

# The task's detail header carries the same line.
Enter
Sleep 3s
Screenshot demos/frames/workers-same-output-detail.png
Sleep 2s
Type "q"
Sleep 300ms
Hide
Type "q"
Sleep 300ms
