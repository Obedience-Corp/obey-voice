# A foreground tool call fails and the turn carries on: the model retries
# with another command and replies, with no error state on screen meanwhile.
# Real TUI against a loopback model (demos/fixtures/tool-failure.py) in a
# disposable HOME. Text mode, no audio, no live inference.
# Run: just demo-tool-failure
Output demos/tool-failure.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1320
Set Height 700
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 15
Set TypingSpeed 30ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}
Hide
Type "python3 demos/fixtures/tool-failure.py"
Enter
Wait+Screen@20s /New conversation/
Enter
Sleep 1s
Enter
Wait+Screen@20s /Type a message/
Sleep 1s
Show
Sleep 1s
Type "List the files in /no/such/dir."
Enter
Wait+Screen@20s /run_command failed/
Sleep 4s
Wait+Screen@20s /notes\.txt\./
Sleep 4s
Hide
Ctrl+C
Sleep 500ms
Ctrl+C
Sleep 1s
