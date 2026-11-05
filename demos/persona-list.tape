# Record the real persona list with an isolated fixture.
# Run: env -u NO_COLOR CLICOLOR_FORCE=1 FORCE_COLOR=1 vhs demos/persona-list.tape
Output demos/persona-list.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 18
Set Width 1220
Set Height 1020
Set Padding 16
Set Framerate 24
Set TypingSpeed 65ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "source demos/fixtures/persona-list.sh" Enter
Sleep 1s
Type "clear" Enter
Show
Type "./bin/obey-voice persona list" Enter
Sleep 6s
