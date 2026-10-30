#!/usr/bin/env python3
"""Record the real TUI with disposable data and a loopback-only model fixture."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


RESPONSES = {
    'demo-obey': 'A local voice agent. Let\'s see what feels good to talk to.',
    'demo-jarvis': 'Measure time to first reply, clean interruptions, and whether it finishes the task.',
    'demo-veronica': 'Try real conversations. A fast answer is no use if it misses the point.',
}
RECAP = 'Jarvis wants measurable results; Veronica wants real conversations. Let\'s test both.'
EVIDENCE = Path('bin/readme-demo-evidence.json')


def verify(evidence):
    calls = evidence['calls']
    expected = ['demo-obey', 'demo-jarvis', 'demo-veronica', 'demo-obey']
    if [call['model'] for call in calls] != expected:
        raise ValueError('The demo did not route all four turns to the expected models')
    if not calls[-1]['shared_history']:
        raise ValueError('Obey did not receive the earlier Jarvis and Veronica replies')
    sessions = evidence['sessions']
    if len(sessions) != 1:
        raise ValueError('Expected one saved shared conversation')
    assistants = [turn for turn in sessions[0]['turns'] if turn['role'] == 'assistant']
    if [turn.get('persona_id') for turn in assistants] != ['obey', 'jarvis', 'veronica', 'obey']:
        raise ValueError('Saved replies lost their persona attribution')
    if [turn['content'] for turn in assistants] != [*RESPONSES.values(), RECAP]:
        raise ValueError('Saved replies do not match the recorded conversation')


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
        self.reply({'models': [{'name': model, 'model': model} for model in RESPONSES]})

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        if request.get('format'):
            self.reply({'message': {'role': 'assistant', 'content':
                json.dumps({'kind': 'none', 'name': '', 'bare': False})}, 'done': True})
            return
        if request.get('options', {}).get('num_predict') == 1:
            self.reply({'message': {'role': 'assistant', 'content': 'Ready.'}, 'done': True})
            return
        model = request.get('model')
        messages = request.get('messages', [])
        latest = next((m['content'] for m in reversed(messages) if m['role'] == 'user'), '')
        history = '\n'.join(m.get('content', '') for m in messages)
        shared = all(RESPONSES[key] in history for key in ('demo-jarvis', 'demo-veronica'))
        content = RESPONSES.get(model, 'Unexpected demo model.')
        if 'Can you see their ideas?' in latest:
            content = RECAP if shared else 'The earlier replies are missing from my context.'
        self.server.calls.append({'model': model, 'shared_history': shared})
        self.send_response(200)
        self.send_header('Content-Type', 'application/x-ndjson')
        self.end_headers()
        try:
            words = content.split(' ')
            for index, word in enumerate(words):
                delta = word + (' ' if index < len(words) - 1 else '')
                self.wfile.write(json.dumps({'model': model, 'message': {
                    'role': 'assistant', 'content': delta}, 'done': False}).encode() + b'\n')
                self.wfile.flush()
                time.sleep(.08)
            self.wfile.write(json.dumps({'model': model, 'message': {
                'role': 'assistant', 'content': ''}, 'done': True,
                'done_reason': 'stop'}).encode() + b'\n')
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass


def main():
    if sys.argv[1:] == ['--verify']:
        verify(json.loads(EVIDENCE.read_text()))
        print('README demo verified: three personas, four replies, shared model context, saved identities.')
        return
    evidence_path = EVIDENCE.resolve()
    evidence_path.unlink(missing_ok=True)
    binary = Path('bin/obey-voice').resolve()
    with tempfile.TemporaryDirectory(prefix='obey-readme-') as temporary:
        demo_dir = Path(temporary)
        config = demo_dir / '.obey/agents/voice/obey-voice'
        config.mkdir(parents=True)
        server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
        server.calls = []
        (config / 'config.yaml').write_text(f'''brain_provider: ollama
ollama_host: http://127.0.0.1:{server.server_port}
ollama_model: demo-obey
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
        # Override all shipped slots to keep model/voice discovery local.
        for persona, voice in [('obey', 'af_heart'), ('jarvis', 'bm_george'),
                               ('veronica', 'af_bella'), ('samantha', 'af_sky'),
                               ('santa', 'am_santa')]:
            directory = config / 'personas' / persona
            directory.mkdir(parents=True)
            model = f'demo-{persona}' if f'demo-{persona}' in RESPONSES else 'demo-obey'
            (directory / 'persona.yaml').write_text(f'''schema: obey-voice.persona.v1
id: {persona}
display_name: {persona.capitalize()}
brain:
  provider: ollama
  model: {model}
tts:
  provider: kokoro
  voice: {voice}
prompts:
  persona: {persona}
''')
        # Only the child uses this disposable home. No credentials, installed
        # models, user config, or shell environment are copied into the fixture.
        environment = {
            'HOME': temporary, 'PATH': os.environ['PATH'],
            'TERM': 'xterm-256color', 'COLORTERM': 'truecolor',
            'CLICOLOR_FORCE': '1', 'FORCE_COLOR': '1',
            'OBEY_VOICE_COLOR_PROFILE': 'ansi',
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
            sessions = [json.loads(path.read_text())
                        for path in sorted((config / 'sessions').glob('*.json'))]
            evidence_path.write_text(json.dumps({'calls': server.calls, 'sessions': sessions}, indent=2))


if __name__ == '__main__':
    main()
