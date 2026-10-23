#!/usr/bin/env python3
"""Record the real TUI with disposable data and a loopback-only model fixture."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, payload):
        data = json.dumps(payload).encode() + b'\n'
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.reply({'models': [{'name': 'demo', 'model': 'demo'}]})

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        content = 'Ready when you are.'
        if request.get('format'):
            content = json.dumps({'kind': 'none', 'name': '', 'bare': False})
        self.reply({'model': 'demo', 'message': {'role': 'assistant', 'content': content},
                    'done': True, 'done_reason': 'stop'})


def main():
    binary = Path('bin/obey-voice').resolve()
    with tempfile.TemporaryDirectory(prefix='obey-readme-') as temporary:
        demo_dir = Path(temporary)
        config = demo_dir / '.obey/agents/voice/festival-voice'
        config.mkdir(parents=True)
        server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
        (config / 'config.yaml').write_text(f'''brain_provider: ollama
ollama_host: http://127.0.0.1:{server.server_port}
ollama_model: demo
agent_name: Obey
active_persona: obey
tts_provider: kokoro
tts_voice: af_heart
vad_enabled: false
talk_hotkey_enabled: false
voice_workers_disabled: true
voice_work_enabled: false
voice_tools_enabled: false
skills_enabled: false
''')
        # Only the child uses this disposable home. No credentials, installed
        # models, user config, or shell environment are copied into the fixture.
        environment = {
            'HOME': temporary, 'PATH': os.environ['PATH'],
            'TERM': 'xterm-256color', 'COLORTERM': 'truecolor',
            'CLICOLOR_FORCE': '1', 'FORCE_COLOR': '1',
            'OBEY_VOICE_COLOR_PROFILE': 'ansi',
            'OBEY_VOICE_DEMO_VOICE_ANIM': '1',
        }
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            with subprocess.Popen([str(binary), '-t', '-n'], cwd=demo_dir,
                                  env=environment) as process:
                try:
                    code = process.wait()
                except KeyboardInterrupt:
                    # VHS sends Ctrl+C to the foreground process group. Give
                    # the TUI time to restore the terminal and save its session.
                    try:
                        code = process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.terminate()
                        code = process.wait(timeout=5)
                if code not in (0, -2):
                    raise SystemExit(code)
        finally:
            server.shutdown()
            server.server_close()


if __name__ == '__main__':
    main()
