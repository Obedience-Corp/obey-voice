# Real TUI: Esc stops a running reply and nothing else starts.
# Run: just demo-esc-stop. No personal config, audio, or live inference.
Output demos/esc-stop.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1200
Set Height 700
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 15
Set TypingSpeed 30ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}
Hide
Type "python3 demos/fixtures/context-meter.py"
Enter
Sleep 3s
Enter
Sleep 1s
Enter
Sleep 4s
Show
Sleep 1s
Type "Summarize the release notes"
Enter
Sleep 3s
Escape
Sleep 2s
Hide
Ctrl+C
Sleep 500ms
Ctrl+C
Sleep 1s
