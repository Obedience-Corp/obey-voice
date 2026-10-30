# Launcher notice — one line on the rail, n opens the session log.
#
# OBEY_VOICE_DEMO_LAUNCHER_NOTICE=route injects a saved bundle, then a
# camp import receipt. q inside Notices returns to the menu.
# From repo root after `just build`:
#   just demo-launcher-notice

Output demos/launcher-notice.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1040
Set Height 720
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 18
Set TypingSpeed 40ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi OBEY_VOICE_DEMO_LAUNCHER_NOTICE=route" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export OBEY_VOICE_DEMO_ROOT=$(mktemp -d /tmp/obey-voice-demo.XXXXXX)" Enter
Type "mkdir -p $OBEY_VOICE_DEMO_ROOT/sessions" Enter
Type "cat > $OBEY_VOICE_DEMO_ROOT/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_model: qwen3.8:latest" Enter
Type "tts_provider: kokoro" Enter
Type "models_dir: /tmp/obey-voice-demo-models" Enter
Type "agent_name: Samantha" Enter
Type "active_persona: samantha" Enter
Type "EOF" Enter
Type "clear" Enter

Type "./bin/demo-terminal.test -test.run '^TestDemoTerminalHarness$'"
Sleep 400ms
Enter
Sleep 5s
Show
Sleep 3s
Screenshot demos/frames/launcher-notice.png
Sleep 500ms
Type "n"
Sleep 1s
Screenshot demos/frames/launcher-notice-open.png
Sleep 500ms
Type "q"
Sleep 400ms
Hide
Type "q"
Sleep 300ms
Hide
Type "rm -rf -- $OBEY_VOICE_DEMO_ROOT" Enter
