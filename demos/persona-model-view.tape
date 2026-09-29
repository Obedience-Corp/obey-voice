# Effective LLM routing in the real persona manager. Disposable fixture catalog.
# Run: just demo-persona-model-view
Output demos/persona-model-view.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1320
Set Height 740
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 35ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}


Hide
Type "bash demos/fixtures/persona-model-view.sh" Enter
Sleep 3s
Down
Sleep 500ms
Enter
Sleep 1s
Up 10
Sleep 500ms
Show
Sleep 3s
Screenshot demos/frames/persona-model-view.png
Sleep 700ms
# Inherited provider and model.
Down
Sleep 3s
Screenshot demos/frames/persona-model-inherited.png
Sleep 700ms
# Explicit provider/model; switch only the disposable fixture's active persona.
Down
Sleep 2s
Enter
Sleep 2s
# Claude leaves model selection to its CLI.
Down 4
Sleep 3s
Screenshot demos/frames/persona-model-claude.png
Sleep 700ms
# Inspect the existing model editor for the local persona.
Up 6
Sleep 1s
Type "m"
Sleep 3s
Screenshot demos/frames/persona-model-edit.png
Sleep 700ms
Escape
Sleep 1s
Escape
Sleep 500ms
Type "q"
Sleep 500ms
