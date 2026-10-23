# Isolated real settings UI. Uses a metadata-only fake Codex; never inference/audio.
Output "/tmp/obey-codex-settings/settings.gif"
Set Shell bash
Set FontSize 18
Set Width 1280
Set Height 900
Set TypingSpeed 1ms
Hide
Type "python3 demos/fixtures/codex-settings.py /tmp/obey-codex-settings" Enter
Sleep 4s
Down@100ms 7
Enter
Sleep 4s
Show
Sleep 1s
Down@100ms 6
Type "+"
Type "+"
Type "+"
Sleep 2s
Screenshot "/tmp/obey-codex-settings/saved.png"
Hide
Ctrl+C
Sleep 1s
