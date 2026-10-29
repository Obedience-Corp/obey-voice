# Managed Qwen preset voices in Settings and per-book Audiobook controls.
#
# Deterministic: demos/fixtures/fake-qwen-native lays down the native package
# layout production code inspects (worker, runtime libs, 0.6b GGUFs, presets,
# a manifest with matching sha256s), without downloading the multi-GB model.
# From repo root:
#   just demo-qwen-voices

Output demos/qwen-voices.gif

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
Type "export TERM=xterm-256color COLORFGBG=15\;0 SAMANTHA_COLOR_PROFILE=ansi" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/samantha-qwen-voices-demo-$$" Enter
Type "export MODELS=$HOME/.cache/samantha/models" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/obey-voice/sessions" Enter
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
# Audiobook, Settings: six down).
Type "obey-voice -t -n" Sleep 300ms Enter
Sleep 1800ms
Down@180ms 6
Sleep 650ms
Enter
Sleep 1100ms

# Settings opens on Brain; 4 jumps to Voice. The Engine list marks kokoro,
# and the Qwen package the fixture installed is the next line down.
Type "4"
Sleep 1300ms
Down
Sleep 700ms
Enter
Sleep 1700ms

# The nine model-native speakers now sit under the engines with the Qwen
# default marked. Move down to Ryan and choose it.
Down@220ms 7
Sleep 1100ms
Enter
Sleep 1800ms

# Return to the launcher: the status badge reflects Qwen + CustomVoice + Ryan.
Escape
Sleep 1800ms

# Open Create audiobook and prove Qwen voice/language are per-book choices.
Up
Sleep 300ms
Enter
Sleep 1500ms
Down
Down
Down
Down
Sleep 700ms
Right
Sleep 900ms
Down
Right
Sleep 1200ms
Down
Right
Sleep 1500ms
Type "q"
Sleep 400ms
