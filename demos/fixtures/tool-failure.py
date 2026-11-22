#!/usr/bin/env python3
"""Run the real TUI against a loopback model whose first tool call fails."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


REPLY = [
    "The folder ", "/no/such/dir ", "does not exist, ", "so I listed ",
    "the current folder ", "instead. ", "It holds ", "one file, ", "notes.txt.",
]


def tool_call(call_id, command):
    return {'message': {'role': 'assistant', 'content': '', 'tool_calls': [
        {'id': call_id, 'function': {'name': 'run_command', 'arguments': {'command': command}}},
    ]}, 'done': True, 'done_reason': 'stop'}


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def begin(self):
        self.send_response(200)
        self.send_header('Content-Type', 'application/x-ndjson')
        self.end_headers()

    def reply(self, payload):
        self.wfile.write(json.dumps(payload).encode() + b'\n')
        self.wfile.flush()

    def do_GET(self):
        self.begin()
        self.reply({'models': [{'name': 'demo', 'model': 'demo'}]})

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))) or b'{}')
        self.begin()
        try:
            if self.path == '/api/show':
                self.reply({'capabilities': ['completion', 'tools'], 'details': {'family': 'demo'}})
                return
            if request.get('options', {}).get('num_predict') == 1 or not request.get('tools'):
                self.reply({'message': {'role': 'assistant', 'content': 'Ready.'}, 'done': True, 'done_reason': 'stop'})
                return
            results = sum(1 for message in request.get('messages', []) if message.get('role') == 'tool')
            if results == 0:
                time.sleep(1)
                self.reply(tool_call('call-1', 'ls /no/such/dir'))
            elif results == 1:
                time.sleep(5)
                self.reply(tool_call('call-2', 'ls *.txt'))
            else:
                time.sleep(2)
                for chunk in REPLY:
                    self.reply({'message': {'role': 'assistant', 'content': chunk}, 'done': False})
                    time.sleep(.15)
                self.reply({'message': {'role': 'assistant', 'content': ''}, 'done': True, 'done_reason': 'stop'})
        except (BrokenPipeError, ConnectionResetError):
            pass


def main():
    binary = Path('bin/obey-voice').resolve()
    with tempfile.TemporaryDirectory(prefix='obey-tool-failure-') as temporary:
        demo_dir = Path(temporary)
        (demo_dir / 'notes.txt').write_text('demo\n')
        config = demo_dir / '.obey/agents/voice/obey-voice'
        config.mkdir(parents=True)
        server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
        (config / 'config.yaml').write_text(f'''brain_provider: ollama
ollama_host: http://127.0.0.1:{server.server_port}
ollama_model: demo
ollama_num_ctx: 8192
agent_name: Obey
active_persona: obey
tts_provider: kokoro
vad_enabled: false
talk_hotkey_enabled: false
voice_workers_disabled: true
voice_work_enabled: false
voice_tools_enabled: true
skills_enabled: false
''')
        environment = {
            'HOME': temporary, 'PATH': os.environ['PATH'],
            'TERM': 'xterm-256color', 'COLORTERM': 'truecolor',
            'CLICOLOR_FORCE': '1', 'FORCE_COLOR': '1',
            'OBEY_VOICE_COLOR_PROFILE': 'truecolor',
        }
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            with subprocess.Popen([str(binary), '-t', '-n'], cwd=demo_dir, env=environment) as process:
                try:
                    code = process.wait()
                except KeyboardInterrupt:
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
