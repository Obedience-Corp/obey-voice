#!/usr/bin/env python3
"""Run the real companion across fresh setup and process restart, without audio.

Only model responses are stubbed. Config writes, setup progress, conversation
controller, session files and resume use production code through its JSONL API.
The desktopfixture build redirects config to a new temporary installation.
"""
import argparse
import json
import os
from pathlib import Path
import queue
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Provider(BaseHTTPRequestHandler):
    available = True
    installed = True

    def log_message(self, *_):
        pass

    def reply(self, value, status=200):
        data = (json.dumps(value) + '\n').encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if not self.available:
            self.reply({'error': 'Fixture provider unavailable'}, 503)
        elif self.path == '/api/tags':
            self.reply({'models': [{'name': 'first-conversation'}] if self.installed else []})
        else:
            self.reply({'error': 'Unexpected endpoint'}, 404)

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        if self.path != '/api/chat':
            self.reply({'error': 'Unexpected endpoint'}, 404)
            return
        content = 'First conversation reply persisted.'
        if request.get('format'):
            content = json.dumps({'kind': 'none', 'name': '', 'bare': False})
        self.reply({'model': 'first-conversation', 'message': {'role': 'assistant', 'content': content},
                    'done': True, 'done_reason': 'stop'})


class Companion:
    def __init__(self, engine, env, evidence, label):
        self.events = queue.Queue()
        self.pending = []
        self.sequence = 0
        self.log = (evidence / (label + '.jsonl')).open('w')
        self.errors = (evidence / (label + '.stderr')).open('w')
        self.process = subprocess.Popen([str(engine), '--bridge'], env=env,
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.errors, text=True, bufsize=1)
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()
        try:
            self.until(lambda row: row.get('event') == 'ready')
        except BaseException as error:
            # Construction has not returned, so the caller cannot close us.
            # Keep the readiness error even if cleanup also reports a failure.
            try:
                self.close()
            except Exception as cleanup_error:
                if hasattr(error, 'add_note'):
                    error.add_note(f'Companion cleanup: {cleanup_error}')
            raise

    def read(self):
        try:
            for line in self.process.stdout:
                self.log.write(line)
                self.log.flush()
                self.events.put(json.loads(line))
        finally:
            self.events.put({'process_eof': True})

    def until(self, predicate):
        deadline = time.monotonic() + 30
        while True:
            for i, row in enumerate(self.pending):
                if predicate(row):
                    return self.pending.pop(i)
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError('Timed out waiting for companion; inspect retained JSONL/stderr')
            try:
                row = self.events.get(timeout=remaining)
            except queue.Empty as exc:
                raise RuntimeError('Timed out waiting for companion') from exc
            if row.get('process_eof'):
                raise RuntimeError('Companion exited before expected response')
            if predicate(row):
                return row
            self.pending.append(row)

    def call(self, method, **fields):
        self.sequence += 1
        self.process.stdin.write(json.dumps({'id': self.sequence, 'method': method, **fields}) + '\n')
        self.process.stdin.flush()
        result = self.until(lambda row: row.get('id') == self.sequence)
        if result.get('error'):
            raise RuntimeError(result['error'])
        return result.get('result')

    def close(self):
        try:
            if self.process.poll() is None:
                try:
                    self.process.stdin.close()
                except BrokenPipeError:
                    pass  # The child exited between poll and closing stdin.
                try:
                    self.process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait()
                    raise RuntimeError('Companion did not exit after EOF')
        finally:
            self.reader.join(timeout=5)
            self.process.stdin.close()
            self.process.stdout.close()
            self.log.close()
            self.errors.close()
        if self.process.returncode:
            raise RuntimeError(f'Companion exited {self.process.returncode}')


def run(evidence):
    root = Path(__file__).resolve().parent.parent
    config = evidence / 'config'
    models = evidence / 'models'
    config.mkdir()
    models.mkdir()
    engine = evidence / 'engine'
    subprocess.run(['go', 'build', '-tags', 'desktopfixture', '-o', str(engine), '.'],
                   cwd=root / 'bridge', check=True)
    env = {key: value for key, value in os.environ.items()
           if not key.startswith(('OBEY_', 'SAMANTHA_'))}
    env['OBEY_VOICE_TEST_CONFIG'] = str(config)
    server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    companion = None
    try:
        companion = Companion(engine, env, evidence, 'fresh')
        status = companion.call('setup_status')
        assert not status['existing'] and not status['progress']['completed'], status
        assert not (config / 'config.yaml').exists()
        companion.close()
        companion = None

        # Real CLI writes, as used by the desktop settings surface.
        settings = {'models_dir': str(models), 'work_dir': str(evidence),
                    'brain_provider': 'ollama', 'ollama_model': 'first-conversation',
                    'ollama_host': f'http://127.0.0.1:{server.server_port}',
                    'vad_enabled': 'false', 'talk_hotkey_enabled': 'false',
                    'voice_workers_disabled': 'true', 'voice_work_enabled': 'false',
                    'voice_tools_enabled': 'false', 'skills_enabled': 'false'}
        for key, value in settings.items():
            subprocess.run([str(engine), 'config', 'set', key, value, '--json'],
                           env=env, cwd=evidence, check=True, capture_output=True, text=True, timeout=30)

        companion = Companion(engine, env, evidence, 'configured')
        Provider.installed = False
        missing = companion.call('setup_provider')
        assert missing['severity'] == 'error' and missing.get('remediation'), missing
        Provider.installed = True
        Provider.available = False
        unavailable = companion.call('setup_provider')
        assert unavailable['severity'] == 'error' and '503' in unavailable['detail'], unavailable
        Provider.available = True
        assert companion.call('setup_provider')['severity'] == 'ok'
        progress = {'version': 1, 'step': 4, 'completed': True, 'deferred': False,
                    'text_only': True, 'silent': True}
        companion.call('setup_save', config=progress)
        session = companion.call('start', text_only=True, silent=True, persona_id=':defaults')['session_id']
        companion.call('text', text='Hello from a fresh installation')
        companion.until(lambda row: row.get('event') == 'state'
                        and 'First conversation reply persisted.' in json.dumps(row.get('data')))
        companion.call('stop')
        saved = json.loads((config / 'sessions' / (session + '.json')).read_text())
        turns = json.dumps(saved['turns'])
        assert 'Hello from a fresh installation' in turns and 'First conversation reply persisted.' in turns
        companion.close()
        companion = None

        companion = Companion(engine, env, evidence, 'restarted')
        assert companion.call('setup_status')['progress'] == progress
        sessions = companion.call('sessions')
        assert session in json.dumps(sessions), sessions
        companion.call('start', text_only=True, silent=True, persona_id=':defaults', session_id=session)
        companion.until(lambda row: row.get('event') == 'state'
                        and 'Hello from a fresh installation' in json.dumps(row.get('data'))
                        and 'First conversation reply persisted.' in json.dumps(row.get('data')))
        companion.call('stop')
        assert not list(models.iterdir()), 'Text-only check unexpectedly populated model cache'
        return {'result': 'pass', 'session_id': session,
                'checks': ['empty setup', 'CLI settings writes', 'missing model', 'provider HTTP 503',
                           'provider recovery', 'text reply', 'session file', 'process restart',
                           'setup persistence', 'session resume', 'empty speech-model cache'],
                'not_verified': ['native window', 'microphone/speaker', 'model downloads',
                                 'real model inference', 'clean-machine dependency installation']}
    finally:
        try:
            if companion is not None:
                companion.close()
        finally:
            server.shutdown()
            server.server_close()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, help='New evidence directory; must not already exist')
    args = parser.parse_args()
    if args.output:
        evidence = args.output.resolve()
        evidence.mkdir(parents=True, exist_ok=False)
    else:
        evidence = Path(tempfile.mkdtemp(prefix='obey-first-conversation-'))
    print(f'Evidence: {evidence}', flush=True)
    report = run(evidence)
    report['commit'] = subprocess.check_output(['git', 'rev-parse', 'HEAD'],
                                               cwd=Path(__file__).resolve().parent, text=True).strip()
    report['working_tree_dirty'] = bool(subprocess.check_output(
        ['git', 'status', '--porcelain'], cwd=Path(__file__).resolve().parent, text=True).strip())
    (evidence / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))
