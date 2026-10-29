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
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: demo-model" Enter
Type "tts_provider: kokoro" Enter
Type "tts_voice: af_heart" Enter
Type "models_dir: $MODELS" Enter
Type "agent_name: Samantha" Enter
Type "EOF" Enter
Type "clear" Enter
Show

# Launcher → Settings (New conversation, Personas, Meeting, Remote, Library,
# Audiobook, Settings: six down). Settings opens on Brain: providers and
# models on one screen, the sidebar naming every section.
Type "obey-voice -t -n" Sleep 300ms Enter
Sleep 1800ms
Down@180ms 6
Sleep 600ms
Enter
Sleep 2200ms

# ↓ moves through the lists; headings are skipped.
Down@350ms 3
Sleep 1300ms

# → is the next section; a digit jumps straight to one.
Right
Sleep 1300ms
Right
Sleep 1300ms
Type "4"
Sleep 1800ms

# Voice: engines, then the voices of the active engine. Choosing the
# installed Qwen engine swaps in its presets; ↓ to Ryan, enter chooses.
Down
Sleep 700ms
Enter
Sleep 1800ms
Down@200ms 7
Sleep 900ms
Enter
Sleep 1700ms

# Audio: microphone and speaker lists, then the volume slider (- / +). The
# cursor stops at the end, however many devices the machine has.
Type "8"
Sleep 1500ms
Down@40ms 60
Sleep 700ms
Type "-"
Sleep 500ms
Type "-"
Sleep 900ms
Type "+"
Sleep 1300ms

# Meeting: toggles, the engine list, choice rows with every option on the
# row, destinations. enter on a choice row moves it on.
Type "9"
Sleep 1900ms
Down@250ms 4
Sleep 700ms
Enter
Sleep 1500ms

# ← goes back a section.
Left
Sleep 1000ms
Left
Sleep 1500ms

Escape
Sleep 1200ms
Type "q"
Sleep 400ms
