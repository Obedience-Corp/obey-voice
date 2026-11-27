# Muse Spark as the brain: Settings → Model lists Muse's model catalog, and the
# choice is saved. Disposable HOME under /tmp; a stand-in muse serves an
# invented catalog over MSP. No inference or audio.

Output "demos/muse-settings.gif"

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
Type "python3 demos/fixtures/muse-settings.py /tmp/obey-muse-settings" Enter
Sleep 3s
Show

# The launcher header reads "model default" for muse-spark.
Sleep 1500ms

# Launcher → Settings (seven down). Brain opens with the Muse catalog under Model.
Down@180ms 7
Sleep 600ms
Enter
Sleep 2500ms

# From the muse-spark provider row down to the last catalog model, then choose it.
Down@450ms 6
Sleep 800ms
Enter
Sleep 2000ms

# Back to the launcher: the header now names the chosen model.
Escape
Sleep 2500ms

Hide
Ctrl+C
Sleep 1s
