#!/usr/bin/env python3
"""Prepare an isolated native UI test app and serve deterministic local replies.

Build/bundle first, run this script, then open the printed app through Computer Use.
A test-only companion redirects the install root and fails closed without an
explicit fixture path. The user's Obey config is never copied. Keep the process running while exercising the UI.
"""
import argparse
import os
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
parser.add_argument("--brain-error", action="store_true", help="Return a provider error for requests containing trigger failure")
parser.add_argument('--group-participation', action='store_true', help='Enable isolated participation controls and deterministic private decisions')
parser.add_argument("--images", action="store_true", help="Enable isolated image/GIF import and vision request checks")
parser.add_argument("--agent-images", action="store_true", help="Return image/GIF files as agent attachments")
args = parser.parse_args()
args.images = args.images or args.agent_images
root = pathlib.Path(__file__).resolve().parent.parent
fixture_root = pathlib.Path(tempfile.mkdtemp(prefix='obey-desktop-ui-'))
config_dir = fixture_root / 'config'
config_dir.mkdir(parents=True)

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def respond(self, value, status=200):
        body = json.dumps(value).encode() + b'\n'
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        self.respond({'models': [{'name': 'desktop-test', 'model': 'desktop-test'}]})
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        if args.images:
            with (fixture_root / 'image-requests.jsonl').open('a') as log:
                log.write(json.dumps(request) + '\n')
            if self.path == '/api/show':
                self.respond({'capabilities': ['completion', 'vision'], 'model_info': {}})
                return
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
        if args.brain_error and not request.get('format') and any(
            m.get('role') == 'user' and 'trigger failure' in m.get('content', '').lower()
            for m in request.get('messages', [])
        ):
            self.respond({'error': 'Fixture provider unavailable (503)'}, status=503)
            return
        content = 'The native desktop conversation is working.'
        if args.images and any(m.get('images') for m in request.get('messages', [])):
            content = 'I received the attached image. The screenshot has colored blocks; GIF input uses its first frame.'
            if args.agent_images:
                content += f'\n![Returned image](<{fixture_root / "Screenshot with spaces.png"}>)\n![Returned animation](<{fixture_root / "animation.gif"}>)'
        if request.get('format'):
            if args.group_participation:
                schema = request['format'].get('properties', {})
                if 'member_keys' in schema:
                    payload = json.loads(request['messages'][-1]['content'])
                    content = json.dumps({'kind': 'group', 'member_keys': [p['key'] for p in payload['participants']], 'coverage': 'all', 'reason': 'explicit', 'unresolved_names': []})
                elif 'intent' in schema:
                    content = json.dumps({'kind': 'speak', 'reason': 'perspective', 'intent': 'Offer a useful view', 'references': []})
                else:
                    content = json.dumps({'kind': 'yield', 'member_key': '', 'reason': 'no_contribution'})
            else:
                content = json.dumps({'kind': 'none', 'name': '', 'bare': False})
        self.respond({'model': 'desktop-test', 'message': {'role': 'assistant', 'content': content}, 'done': True, 'done_reason': 'stop'})

server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
fixture_config = f'''work_dir: {fixture_root}
brain_provider: ollama
ollama_host: http://127.0.0.1:{server.server_port}
ollama_model: desktop-test
vad_enabled: false
talk_hotkey_enabled: false
voice_workers_disabled: true
voice_work_enabled: false
voice_tools_enabled: false
skills_enabled: false
'''
if args.group_participation:
    fixture_config += 'group_participation_enabled: true\ngroup_participation_mode: invited\n'
if args.voice_preview:
    fixture_config += f'tts_provider: openai-speech\nopenai_speech_url: http://127.0.0.1:{server.server_port}/v1\nopenai_speech_voice: fixture-tone\n'
if not args.fresh:
    (config_dir / 'config.yaml').write_text(fixture_config)
fixture_app = root / ('dist/Obey Voice Agent Images Test.app' if args.agent_images else 'dist/Obey Voice Images Test.app' if args.images else 'dist/Obey Voice Test.app')
if args.images:
    env = dict(os.environ, OBEY_VOICE_MEDIA_FIXTURE_DIR=str(fixture_root))
    subprocess.run(['go', 'test', './internal/testmedia', '-run', '^TestWriteFixtures$', '-count=1'], cwd=root.parent.parent, env=env, check=True)
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
if args.images:
    info.update(CFBundleIdentifier='com.obedience.voice-agent.images-test', CFBundleName='Obey Voice Images Test', CFBundleDisplayName='Obey Voice Images Test')
if args.agent_images:
    info.update(CFBundleIdentifier='com.obedience.voice-agent.agent-images-test', CFBundleName='Obey Voice Agent Images Test', CFBundleDisplayName='Obey Voice Agent Images Test')
if args.group_participation or args.images:
    launcher = fixture_app / 'Contents/MacOS/fixture-launcher'
    # A Mach-O launcher also keeps resume text-only; LaunchServices does not
    # reliably launch a shell script as CFBundleExecutable.
    subprocess.run(['cc', '-x', 'c', '-', '-o', str(launcher)], input=r'''
#include <mach-o/dyld.h>
#include <string.h>
#include <unistd.h>
int main(void) {
    char path[4096]; uint32_t size = sizeof(path);
    if (_NSGetExecutablePath(path, &size) != 0) return 1;
    char *tail = strrchr(path, '/');
    if (!tail || (size_t)(tail - path) + sizeof("/obey-voice-agent") > sizeof(path)) return 1;
    strcpy(tail, "/obey-voice-agent");
    char *args[] = {path, "--text-only", "--silent", NULL};
    execv(path, args);
    return 1;
}
''', text=True, check=True)
    info['CFBundleExecutable'] = 'fixture-launcher'
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
