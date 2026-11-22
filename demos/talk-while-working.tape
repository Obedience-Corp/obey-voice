# Talking to another persona while one is working.
#
# Anika writes a long reply on a local Ollama model. While she is still
# writing, a typed "Hey Obey, …" is answered by Obey; Anika's reply keeps
# going as her job and arrives later under her name. Text mode, no voice,
# no talk hotkey, disposable HOME (a fresh folder per run under
# /tmp/obey-voice-talk-while-working-demo). Both personas share one model,
# which must already be installed.
#
# From repo root after `just build`:
#   just demo-talk-while-working

Output demos/talk-while-working.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 14
Set Width 1180
Set Height 820
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 12
Set TypingSpeed 35ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/obey-voice-talk-while-working-demo/$$" Enter
Type "export OV=$HOME/.obey/agents/voice/obey-voice" Enter
Type "mkdir -p $OV/personas/anika $OV/personas/obey $OV/prompts/persona $OV/prompts/turn $OV/sessions" Enter
Type "cat > $OV/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: inkling-2-mini-uncensored:latest" Enter
Type "ollama_num_ctx: 32768" Enter
Type "tts_provider: kokoro" Enter
Type "agent_name: Anika" Enter
Type "active_persona: anika" Enter
Type "talk_hotkey_enabled: false" Enter
Type "skills_enabled: false" Enter
Type "EOF" Enter
Type "cat > $OV/personas/anika/persona.yaml <<'EOF'" Enter
Type "schema: obey-voice.persona.v1" Enter
Type "id: anika" Enter
Type "display_name: Anika" Enter
Type "brain:" Enter
Type "  provider: ollama" Enter
Type "  model: inkling-2-mini-uncensored:latest" Enter
Type "prompts:" Enter
Type "  persona: anika" Enter
Type "  turn: anika" Enter
Type "EOF" Enter
Type "cat > $OV/personas/obey/persona.yaml <<'EOF'" Enter
Type "schema: obey-voice.persona.v1" Enter
Type "id: obey" Enter
Type "display_name: Obey" Enter
Type "brain:" Enter
Type "  provider: ollama" Enter
Type "  model: inkling-2-mini-uncensored:latest" Enter
Type "prompts:" Enter
Type "  persona: obey" Enter
Type "EOF" Enter
Type "cat > $OV/prompts/persona/anika.yaml <<'EOF'" Enter
Type "schema: obey-voice.prompt.v1" Enter
Type "prompt:" Enter
Type "  name: anika" Enter
Type "  kind: persona" Enter
Type "  system_prompt: |-" Enter
Type "    You are {agent_name}, a thoughtful writer. Speak only as yourself." Enter
Type "EOF" Enter
Type "cat > $OV/prompts/turn/anika.yaml <<'EOF'" Enter
Type "schema: obey-voice.prompt.v1" Enter
Type "prompt:" Enter
Type "  name: anika" Enter
Type "  kind: turn" Enter
Type "  system_prompt: |-" Enter
Type "    Respond as {agent_name}. When asked to write something, write it in full in plain paragraphs, with no markdown." Enter
Type "EOF" Enter
Type "cat > $OV/prompts/persona/obey.yaml <<'EOF'" Enter
Type "schema: obey-voice.prompt.v1" Enter
Type "prompt:" Enter
Type "  name: obey" Enter
Type "  kind: persona" Enter
Type "  system_prompt: |-" Enter
Type "    You are {agent_name}, a brief and friendly assistant. Speak only as yourself and answer in one short sentence." Enter
Type "EOF" Enter
Type "cd $HOME && clear" Enter

# Text mode, no voice: launcher → New conversation → Anika.
Type "obey-voice -t -n" Enter
Wait+Screen@20s /New conversation/
Show
Sleep 1500ms
Enter
Sleep 1500ms
Down
Sleep 800ms
Enter
Wait+Screen@20s /Type a message/
Sleep 1s

# A request that makes Anika write for a while.
Type "Write about 500 words on the history of lighthouses."
Sleep 500ms
Enter
Wait+Screen@90s /● Anika/
Sleep 3s

# While she is still writing, address Obey.
Type "Hey Obey, what is two plus two?"
Sleep 500ms
Enter

# The tape stops here unless Anika's reply moved to her job, which it does
# only while she is still writing.
Wait+Screen@20s /continues as job-1/

# Obey answers; Anika's reply is a running job row meanwhile.
Wait+Screen@90s /● Obey/
Sleep 5s

# Anika's reply finishes and arrives under her name.
Wait+Screen@180s /Workers: idle/
Sleep 5s

# Scroll back to where her two parts meet; the job row now shows done.
PageUp
Sleep 1s
PageUp
Sleep 6s
PageDown
Sleep 1s
PageDown
Sleep 2s

Hide
Type "/menu"
Enter
Sleep 3s
Type "q"
Sleep 1s
