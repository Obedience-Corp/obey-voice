#!/usr/bin/env python3
"""Run the real TUI against a slow loopback model, with disposable config."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


STORY = [
    "Mara reached the neon city with a folded map and one promise to keep. ",
    "Rain turned the street into a mirror of violet signs and blue windows. ",
    "At the station, a clockmaker offered to repair her compass in exchange for a story. ",
    "She told him about the lanterns that guided travelers across the salt flats. ",
    "He listened, wound the compass, and pointed toward a bridge hidden beneath the tracks. ",
    "Mara crossed it at dawn, carrying the map toward the lights beyond the river. ",
]


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
        request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        self.begin()
        try:
            if request.get('options', {}).get('num_predict') == 1:
                self.reply({'message': {'role': 'assistant', 'content': 'Ready.'}, 'done': True})
            elif request.get('stream') is False:
                # The real /compact path calls ThinkFull and reseeds the summary.
                time.sleep(2)
                self.reply({'message': {'role': 'assistant', 'content':
                    'Mara crossed the neon city with a map. A clockmaker repaired her compass after hearing her story. She crossed the hidden bridge at dawn.'},
                    'done': True, 'done_reason': 'stop'})
            else:
                for index in range(36):
                    self.reply({'message': {'role': 'assistant', 'content': STORY[index % len(STORY)]}, 'done': False})
                    time.sleep(.22)
                self.reply({'message': {'role': 'assistant', 'content': 'The end.'}, 'done': True, 'done_reason': 'stop'})
        except (BrokenPipeError, ConnectionResetError):
            pass


def main():
    binary = Path('bin/obey-voice').resolve()
    with tempfile.TemporaryDirectory(prefix='obey-context-meter-') as temporary:
        demo_dir = Path(temporary)
        config = demo_dir / '.obey/agents/voice/obey-voice'
        config.mkdir(parents=True)
        server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
        (config / 'config.yaml').write_text(f'''brain_provider: ollama
ollama_host: http://127.0.0.1:{server.server_port}
ollama_model: demo
ollama_num_ctx: 8192
continuity_num_ctx_ceil: 8192
agent_name: Obey
active_persona: obey
tts_provider: kokoro
vad_enabled: false
talk_hotkey_enabled: false
voice_workers_disabled: true
voice_work_enabled: false
voice_tools_enabled: false
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
