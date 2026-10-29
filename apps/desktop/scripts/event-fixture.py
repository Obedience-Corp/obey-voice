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
mode = parser.add_mutually_exclusive_group()
mode.add_argument('--setup', action='store_true', help='Replay setup download and provider recovery')
mode.add_argument('--voice-catalog', action='store_true', help='Replay optional voice descriptions in setup')
mode.add_argument('--work', action='store_true', help='Replay background worker snapshots')
args = parser.parse_args()
args.setup = args.setup or args.voice_catalog
root = Path(__file__).resolve().parent.parent
fixture = Path(tempfile.mkdtemp(prefix='obey-events-'))
(fixture / '.fixture').touch()
if args.voice_catalog:
    (fixture / 'optional-catalog').touch()
(fixture / 'bin').mkdir()
camp = fixture / 'bin/camp'
camp.write_text('#!/bin/sh\nprintf \'[{"name":"Fixture"},{"name":"Unavailable"}]\\n\'\n')
camp.chmod(0o755)
name = 'Obey Voice Setup Replay' if args.setup else ('Obey Voice Work Replay' if args.work else 'Obey Voice Events')
app = root / f'dist/{name}.app'
shutil.copytree(root / 'dist/Obey Voice Agent.app', app, dirs_exist_ok=True)
engine = app / 'Contents/MacOS/event-engine'
shutil.copy2(root / ('scripts/setup-engine.py' if args.setup else 'scripts/event-engine.py'), engine)
engine.chmod(0o755)
path = app / 'Contents/Info.plist'
with path.open('rb') as f:
    info = plistlib.load(f)
suffix = 'setup-replay' if args.setup else ('work-replay' if args.work else 'events')
info.update(CFBundleIdentifier=f'com.obedience.voice-agent.{suffix}', CFBundleName=name)
info['CFBundleDisplayName'] = info['CFBundleName']
info['LSEnvironment'] = {'OBEY_VOICE_ENGINE': str(engine), 'OBEY_VOICE_EVENT_FIXTURE': str(fixture),
                       'OBEY_VOICE_LOG_DIR': str(fixture / 'logs'), 'OBEY_VOICE_TRACE_UI': '1', 'OBEY_VOICE_TOOL_PATH': str(fixture / 'bin') + ':/usr/bin:/bin:/opt/homebrew/bin:/usr/local/bin'}
if args.work:
    info['LSEnvironment']['OBEY_VOICE_WORK_REPLAY'] = '1'
with path.open('wb') as f:
    plistlib.dump(info, f)
subprocess.run(['codesign', '--force', '--deep', '--sign', '-', str(app)], check=True)
print(json.dumps({'app': str(app), 'fixture': str(fixture)}))
