#!/usr/bin/env python3
"""Build a local replay app for UI error, live-meeting, and routing checks."""
import argparse
import json
from pathlib import Path
import plistlib
import shutil
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument('--setup', action='store_true', help='Replay setup download and provider recovery')
args = parser.parse_args()
root = Path(__file__).resolve().parent.parent
fixture = Path(tempfile.mkdtemp(prefix='obey-events-'))
(fixture / '.fixture').touch()
(fixture / 'bin').mkdir()
camp = fixture / 'bin/camp'
camp.write_text('#!/bin/sh\nprintf \'[{"name":"Fixture"},{"name":"Unavailable"}]\\n\'\n')
camp.chmod(0o755)
app = root / ('dist/Obey Voice Setup Replay.app' if args.setup else 'dist/Obey Voice Events.app')
shutil.copytree(root / 'dist/Obey Voice Agent.app', app, dirs_exist_ok=True)
engine = app / 'Contents/MacOS/event-engine'
shutil.copy2(root / ('scripts/setup-engine.py' if args.setup else 'scripts/event-engine.py'), engine)
engine.chmod(0o755)
path = app / 'Contents/Info.plist'
with path.open('rb') as f:
    info = plistlib.load(f)
info.update(CFBundleIdentifier='com.obedience.voice-agent.setup-replay' if args.setup else 'com.obedience.voice-agent.events', CFBundleName='Obey Voice Setup Replay' if args.setup else 'Obey Voice Events')
info['CFBundleDisplayName'] = info['CFBundleName']
info['LSEnvironment'] = {'OBEY_VOICE_ENGINE': str(engine), 'OBEY_VOICE_EVENT_FIXTURE': str(fixture),
                       'OBEY_VOICE_LOG_DIR': str(fixture / 'logs'), 'OBEY_VOICE_TRACE_UI': '1', 'OBEY_VOICE_TOOL_PATH': str(fixture / 'bin') + ':/usr/bin:/bin:/opt/homebrew/bin:/usr/local/bin'}
with path.open('wb') as f:
    plistlib.dump(info, f)
subprocess.run(['codesign', '--force', '--deep', '--sign', '-', str(app)], check=True)
print(json.dumps({'app': str(app), 'fixture': str(fixture)}))
