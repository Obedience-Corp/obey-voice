#!/usr/bin/env python3
"""Prepare an isolated native UI test app and serve deterministic local replies.

Build/bundle first, run this script, then open the printed app through Computer Use.
A test-only companion redirects the install root and fails closed without an
explicit fixture path. The user's Obey config is never copied. Keep the process running while exercising the UI.
"""
import argparse
import json
import math
import pathlib
import plistlib
import shutil
import subprocess
import struct
import tempfile
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

parser = argparse.ArgumentParser()
parser.add_argument('--fresh', action='store_true', help='Start without config.yaml or setup progress')
parser.add_argument('--voice-preview', action='store_true', help='Serve test tones for persona previews; voice broken fails, slow delays')
args = parser.parse_args()
root = pathlib.Path(__file__).resolve().parent.parent
fixture_root = pathlib.Path(tempfile.mkdtemp(prefix='obey-desktop-ui-'))
config_dir = fixture_root / 'config'
config_dir.mkdir(parents=True)

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def respond(self, value):
        body = json.dumps(value).encode() + b'\n'
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        self.respond({'models': [{'name': 'desktop-test', 'model': 'desktop-test'}]})
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        if args.voice_preview and self.path == '/v1/audio/speech':
            with (fixture_root / 'preview-requests.jsonl').open('a') as log:
                log.write(json.dumps(request) + '\n')
            if request.get('voice') == 'broken':
                self.send_error(503, 'Fixture voice unavailable')
                return
            if request.get('voice') == 'slow':
                time.sleep(5)
            # Quiet three-second tone; proves the playback path without models.
            pcm = b''.join(struct.pack('<h', int(600 * math.sin(2 * math.pi * 440 * i / 24000))) for i in range(72000))
            self.send_response(200)
            self.send_header('Content-Type', 'application/octet-stream')
            self.send_header('Content-Length', str(len(pcm)))
            self.end_headers()
            try:
                self.wfile.write(pcm)
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        content = 'The native desktop conversation is working.'
        if request.get('format'):
            content = json.dumps({'kind': 'none', 'name': '', 'bare': False})
        self.respond({'model': 'desktop-test', 'message': {'role': 'assistant', 'content': content}, 'done': True, 'done_reason': 'stop'})

server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
fixture_config = f'''brain_provider: ollama
ollama_host: http://127.0.0.1:{server.server_port}
ollama_model: desktop-test
vad_enabled: false
talk_hotkey_enabled: false
voice_workers_disabled: true
voice_work_enabled: false
voice_tools_enabled: false
skills_enabled: false
'''
if args.voice_preview:
    fixture_config += f'tts_provider: openai-speech\nopenai_speech_url: http://127.0.0.1:{server.server_port}/v1\nopenai_speech_voice: fixture-tone\n'
if not args.fresh:
    (config_dir / 'config.yaml').write_text(fixture_config)
fixture_app = root / 'dist/Obey Voice Test.app'
shutil.copytree(root / 'dist/Obey Voice Agent.app', fixture_app, dirs_exist_ok=True)
subprocess.run(['go', 'build', '-tags', 'desktopfixture', '-o', str(fixture_app / 'Contents/MacOS/obey-voice-engine'), '.'], cwd=root / 'bridge', check=True)
fixture_engine = fixture_app / 'Contents/MacOS/obey-voice-engine'
commands = subprocess.check_output(['otool', '-l', str(fixture_engine)], text=True).splitlines()
for i, line in enumerate(commands):
    if line.strip() == 'cmd LC_RPATH':
        source = commands[i + 2].strip().removeprefix('path ').split(' (offset')[0]
        subprocess.run(['install_name_tool', '-delete_rpath', source, str(fixture_engine)], check=True)
subprocess.run(['install_name_tool', '-add_rpath', '@executable_path/../Frameworks', str(fixture_engine)], check=True)
plist_path = fixture_app / 'Contents/Info.plist'
with plist_path.open('rb') as f:
    info = plistlib.load(f)
info.update(CFBundleIdentifier='com.obedience.voice-agent.test', CFBundleName='Obey Voice Test', CFBundleDisplayName='Obey Voice Test')
info['LSEnvironment'] = {'OBEY_VOICE_TEST_CONFIG': str(config_dir), 'OBEY_VOICE_LOG_DIR': str(fixture_root / 'logs')}
with plist_path.open('wb') as f:
    plistlib.dump(info, f)
subprocess.run(['codesign', '--force', '--deep', '--sign', '-', str(fixture_app)], check=True)
print(json.dumps({'app': str(fixture_app), 'data': str(config_dir), 'provider_url': f'http://127.0.0.1:{server.server_port}'}), flush=True)
try:
    server.serve_forever()
except KeyboardInterrupt:
    pass
finally:
    server.server_close()
