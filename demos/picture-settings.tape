# Real Settings → Tools picture-maker rows in a throwaway HOME: choose
# ComfyUI, then change its address. Ollama points at a closed port and no
# picture is made; the fixture fails unless both values were saved.
# Run from repo root: just demo-picture-settings
Output demos/picture-settings.gif
Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 800
Set TypingSpeed 60ms
Hide
Type "python3 demos/fixtures/picture-settings.py" Enter
Sleep 4s
Down@100ms 7
Enter
Sleep 3s
Type "3"
Sleep 500ms
Show
Sleep 1s
Down@200ms 9
Sleep 2500ms
Enter
Sleep 2500ms
Down
Sleep 1500ms
Enter
Sleep 800ms
Backspace@30ms 21
Type "http://studio.local:8188"
Sleep 800ms
Enter
Sleep 2500ms
Down
Sleep 2500ms
Screenshot demos/frames/picture-settings.png
Hide
Ctrl+C
Sleep 1500ms
