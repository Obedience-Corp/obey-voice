# Persona form: the Work row's inline choice and the Job control row, saved
# to a disposable persona. No model, audio, or credentials.
# Run: just demo-persona-work-rows
Output demos/persona-work-rows.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 14
Set Width 1500
Set Height 860
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 35ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "bash demos/fixtures/persona-work-rows.sh" Enter
Sleep 3s
Down
Sleep 500ms
Enter
Sleep 1s
Down 2
Sleep 500ms
Show
Sleep 1500ms
Type "e"
Sleep 1500ms
# Work: own, worker and inline, each with its help line.
Down@300ms 3
Sleep 3s
Right
Sleep 1500ms
Right
Sleep 3s
# Job control: (default), on and off.
Down@300ms 3
Sleep 3s
Right
Sleep 1500ms
Right
Sleep 1500ms
Left
Sleep 2s
Ctrl+J
Sleep 2500ms
Escape
Sleep 500ms
Escape
Sleep 500ms
Type "q"
Sleep 2500ms
