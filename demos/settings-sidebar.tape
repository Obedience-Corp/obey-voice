# Settings: a sidebar of sections beside the section's lists — ↑/↓ move,
# ←/→ or a digit switch section, enter chooses the option under the cursor.
# Disposable HOME under /tmp; the fake Qwen fixture gives Voice a preset list
# without a download.

Output "demos/settings-sidebar.gif"

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1120
Set Height 660
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 35ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#8be9fd","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0 OBEY_VOICE_COLOR_PROFILE=truecolor" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "rm -rf /tmp/obey-voice-settings-demo && mkdir -p /tmp/obey-voice-settings-demo/.obey/agents/voice/obey-voice/sessions" Enter
Type "export HOME=/tmp/obey-voice-settings-demo MODELS=/tmp/obey-voice-settings-demo/.cache/samantha/models" Enter
Type "demos/fixtures/fake-qwen-native $MODELS" Enter
Type "cat > $HOME/.obey/agents/voice/obey-voice/config.yaml <<EOF" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://127.0.0.1:1" Enter
Type "ollama_model: demo-model" Enter
Type "tts_provider: kokoro" Enter
Type "tts_voice: af_heart" Enter
Type "openai_speech_url: http://127.0.0.1:1/v1" Enter
Type "models_dir: $MODELS" Enter
Type "agent_name: Samantha" Enter
Type "EOF" Enter
Hide
Type "cd /tmp/obey-voice-settings-demo" Enter
Type "clear" Enter
Show

# Launcher → Settings (New conversation, Personas, Workers, Meeting, Remote,
# Library, Audiobook, Settings: seven down). Settings opens on Brain: providers and
# models on one screen, the sidebar naming every section.
Type "obey-voice -t -n" Sleep 300ms Enter
Sleep 1800ms
Down@180ms 7
Sleep 600ms
Enter
Sleep 2200ms

# Browse these sections by their digit shortcuts without changing settings.
Type "2"
Sleep 1300ms
Type "3"
Sleep 1300ms
Type "4"
Sleep 1800ms

# Meeting: toggles, the engine list, choice rows with every option on the
# row, destinations. enter on a choice row moves it on.
Type "9"
Sleep 1900ms

Escape
Sleep 1200ms
Type "q"
Sleep 400ms
