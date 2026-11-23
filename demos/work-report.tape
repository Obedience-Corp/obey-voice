# A running background job is asked for its report with /work report. The
# command prints what the job had done at once, the job's row reads
# "reporting" while its worker writes the answer, and the answer arrives cut
# short with "report requested". OBEY_VOICE_DEMO_WORK_CUE=report scripts the
# job's events and the worker's side of the report; the /work report command,
# its receipt line and the job row are the real conversation TUI. No
# microphone, model, or network is needed.
# Run: just demo-work-report

Output demos/work-report.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 17
Set Width 1200
Set Height 700
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 45ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export OBEY_VOICE_COLOR_PROFILE=ansi OBEY_VOICE_DEMO_WORK_CUE=report" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/obey-voice-work-report-demo-$$" Enter
Type "mkdir -p $HOME/.obey/agents/voice/obey-voice/sessions" Enter
# A stand-in Ollama that only lists one model; the demo sends it no requests.
Type "cat > $HOME/ollama-stub.py <<'EOF'" Enter
Type "import http.server, json" Enter
Type "class H(http.server.BaseHTTPRequestHandler):" Enter
Type "    def do_GET(self): self.reply()" Enter
Type "    def do_POST(self): self.reply()" Enter
Type "    def reply(self):" Enter
Type "        body = json.dumps({'models': [{'name': 'example-model'}], 'capabilities': ['completion', 'tools'], 'details': {'family': 'example'}}).encode()" Enter
Type "        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers(); self.wfile.write(body)" Enter
Type "    def log_message(self, *a): pass" Enter
Type "http.server.HTTPServer(('127.0.0.1', 18434), H).serve_forever()" Enter
Type "EOF" Enter
Type "python3 $HOME/ollama-stub.py & STUB=$!" Enter
Type "cat > $HOME/.obey/agents/voice/obey-voice/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://127.0.0.1:18434" Enter
Type "ollama_model: example-model" Enter
Type "tts_provider: kokoro" Enter
Type "agent_name: Assistant" Enter
Type "voice_workers_disabled: false" Enter
Type "EOF" Enter
Type "clear" Enter

Type "./bin/obey-voice -t -n"
Sleep 400ms
Enter
Sleep 5s
Show
Sleep 1s

# Launcher → New conversation → first persona.
Enter
Sleep 1200ms
Enter
Sleep 6500ms
Screenshot demos/frames/work-report-running.png
Sleep 800ms

# Ask the running job for its report from the keyboard.
Type "/work report job-1"
Sleep 600ms
Enter
Sleep 500ms
Screenshot demos/frames/work-report-reporting.png
Sleep 4500ms
Screenshot demos/frames/work-report-done.png
Sleep 2s
Hide
Ctrl+C
Sleep 500ms
Type "kill $STUB" Enter
