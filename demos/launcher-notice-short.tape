# Short terminal — the notice list windows so the selected detail and the
# copy/back line stay on screen. OBEY_VOICE_DEMO_LAUNCHER_NOTICE=scroll
# seeds 13 notices.
# From repo root after `just build`:
#   just demo-launcher-notice

Output demos/launcher-notice-short.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1040
Set Height 260
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 18
Set TypingSpeed 40ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi OBEY_VOICE_DEMO_LAUNCHER_NOTICE=scroll" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/obey-voice-launcher-notice-short-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/festival-voice/sessions" Enter
Type "cat > $HOME/.obey/agents/voice/festival-voice/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_model: qwen3.8:latest" Enter
Type "tts_provider: kokoro" Enter
Type "agent_name: Samantha" Enter
Type "active_persona: samantha" Enter
Type "EOF" Enter
Type "clear" Enter

Type "./bin/obey-voice -t -n"
Sleep 400ms
Enter
Sleep 5s
Show
Sleep 2s
Type "n"
Sleep 1s
Screenshot demos/frames/launcher-notice-short.png
Sleep 400ms
Type "jjjjjjjj"
Sleep 1s
Screenshot demos/frames/launcher-notice-short-scrolled.png
Sleep 400ms
Type "q"
Sleep 400ms
Type "q"
Sleep 300ms
