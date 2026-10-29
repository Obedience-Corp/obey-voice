# Persona form: p previews the voice; Tab lists {variables}.
#
# From repo root after `just build`:
#   just demo-persona-form-ux

Output demos/persona-form-ux.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 14
Set Width 1180
Set Height 680
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 35ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi SAMANTHA_COLOR_PROFILE=ansi" Enter
Type "export HOME=/tmp/obey-voice-persona-form-ux-demo-$$" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/obey-voice/personas/samantha $HOME/.obey/agents/voice/obey-voice/sessions" Enter
Type "cat > $HOME/.obey/agents/voice/obey-voice/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: demo-model" Enter
Type "tts_provider: kokoro" Enter
Type "tts_voice: af_heart" Enter
Type "user_display_name: Lance" Enter
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
Show

Type "obey-voice -t -n" Sleep 300ms Enter
Sleep 2s
Down
Sleep 500ms
Enter
Sleep 1200ms

Type "n"
Sleep 800ms
Down
Sleep 200ms
Down
Sleep 200ms
Down
Sleep 200ms
Down
Sleep 1400ms
Type "p"
Sleep 2s
Down
Sleep 200ms
Down
Sleep 400ms
Enter
Sleep 800ms
Tab
Sleep 3s
Escape
Sleep 400ms
Type ":q"
Enter
Sleep 600ms
Type "q"
Sleep 300ms
Type "q"
Sleep 300ms
