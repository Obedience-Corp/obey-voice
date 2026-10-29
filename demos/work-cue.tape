# Background work stays visible while Jarvis listens and answers another turn.
# OBEY_VOICE_DEMO_WORK_CUE=1 scripts conversation events and a fixture worker;
# no microphone, model, or network is needed. Run: just demo-work-cue

Output demos/work-cue.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 17
Set Width 1200
Set Height 700
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 45ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi OBEY_VOICE_DEMO_WORK_CUE=1" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/obey-voice-work-cue-demo-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/obey-voice/sessions" Enter
Type "cat > $HOME/.obey/agents/voice/obey-voice/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: gemma4:26b" Enter
Type "tts_provider: kokoro" Enter
Type "agent_name: Jarvis" Enter
Type "active_persona: jarvis" Enter
Type "persona: jarvis" Enter
Type "voice_workers_disabled: false" Enter
Type "EOF" Enter
Type "clear" Enter

Type "./bin/obey-voice -t -n"
Sleep 400ms
Enter
Sleep 5s
Show
Sleep 1s

# Launcher → New conversation → Jarvis (first named persona after defaults).
Enter
Sleep 1200ms
Down
Sleep 300ms
Enter
Sleep 4s
Screenshot demos/frames/work-cue-listening.png
Sleep 6s
Screenshot demos/frames/work-cue-followup.png
Sleep 700ms

# Ctrl+W selects the single active worker and opens its detail.
Ctrl+W
Sleep 2s
Screenshot demos/frames/work-cue-detail.png
Sleep 700ms
Escape
Sleep 400ms
Escape
Sleep 1600ms
