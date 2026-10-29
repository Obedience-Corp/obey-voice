# Conversation TUI — toggle thinking blocks in the thread.
#
# Story:
#   1. Live thinking streams as a dim block
#   2. ResponseReady hides it (speakable-only default)
#   3. Ctrl+K reveals the thinking block above the reply
#   4. Ctrl+K hides it again
#
# OBEY_VOICE_DEMO_THINK=1 scripts the bus events (no live mic/model).
# From repo root after `just build`:
#   just demo-think-blocks

Output demos/think-blocks.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1000
Set Height 560
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 45ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi OBEY_VOICE_DEMO_THINK=1" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/obey-voice-think-demo-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/obey-voice/sessions $HOME/.obey/agents/voice/obey-voice/personas/samantha" Enter
Type "cat > $HOME/.obey/agents/voice/obey-voice/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: gemma4:26b" Enter
Type "tts_provider: kokoro" Enter
Type "tts_voice: af_heart" Enter
Type "agent_name: Samantha" Enter
Type "active_persona: samantha" Enter
Type "persona: samantha" Enter
Type "EOF" Enter
Type "cat > $HOME/.obey/agents/voice/obey-voice/personas/samantha/persona.yaml <<'EOF'" Enter
Type "schema: obey-voice.persona.v1" Enter
Type "id: samantha" Enter
Type "display_name: Samantha" Enter
Type "builtin: true" Enter
Type "tts:" Enter
Type "  provider: kokoro" Enter
Type "  voice: af_heart" Enter
Type "prompts:" Enter
Type "  persona: samantha" Enter
Type "  turn: samantha" Enter
Type "EOF" Enter
Type "clear" Enter

Type "./bin/obey-voice -t -n"
Sleep 400ms
Enter
Sleep 6s
Show
Sleep 2s

# New conversation → persona picker (Use defaults) → start
Enter
Sleep 1200ms
Enter
# Runtime prepare, then scripted think → reply (~6s) and hold (~8s).
Sleep 18s
Ctrl+K
Sleep 4s
Ctrl+K
Sleep 2s
Type "q"
Sleep 400ms
Type "q"
Sleep 300ms
