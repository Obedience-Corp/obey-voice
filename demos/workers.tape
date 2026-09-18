# Workers manager — pretty JSON activity on a live job.
#
# OBEY_VOICE_DEMO_WORKERS=1 injects a fixture job (no live model).
# From repo root after `just build`:
#   just demo-workers

Output demos/workers.gif

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
Type "export OBEY_VOICE_COLOR_PROFILE=ansi OBEY_VOICE_DEMO_WORKERS=1" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/obey-voice-workers-demo-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/festival-voice/sessions" Enter
Type "cat > $HOME/.obey/agents/voice/festival-voice/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: gemma4:26b" Enter
Type "tts_provider: kokoro" Enter
Type "agent_name: Samantha" Enter
Type "active_persona: samantha" Enter
Type "voice_workers_disabled: false" Enter
Type "EOF" Enter
Type "clear" Enter

Type "./bin/obey-voice -t -n"
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
Sleep 1500ms

# Open the running job detail.
Down
Sleep 200ms
Down
Sleep 200ms
Enter
Sleep 4s
Screenshot demos/frames/workers-detail.png
Sleep 2s
Type "q"
Sleep 300ms
Type "q"
Sleep 300ms
