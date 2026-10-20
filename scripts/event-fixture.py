#!/usr/bin/env python3
"""Build a local replay app for UI error, live-meeting, and routing checks."""
import json
from pathlib import Path
import plistlib
import shutil
import subprocess
import tempfile

root = Path(__file__).resolve().parent.parent
fixture = Path(tempfile.mkdtemp(prefix='obey-events-'))
(fixture / '.fixture').touch()
(fixture / 'bin').mkdir()
camp = fixture / 'bin/camp'
camp.write_text('#!/bin/sh\nprintf \'[{"name":"Fixture"},{"name":"Unavailable"}]\\n\'\n')
camp.chmod(0o755)
app = root / 'dist/Obey Voice Events.app'
shutil.copytree(root / 'dist/Obey Voice Agent.app', app, dirs_exist_ok=True)
engine = app / 'Contents/MacOS/event-engine'
shutil.copy2(root / 'scripts/event-engine.py', engine)
engine.chmod(0o755)
path = app / 'Contents/Info.plist'
with path.open('rb') as f:
    info = plistlib.load(f)
info.update(CFBundleIdentifier='com.obedience.voice-agent.events', CFBundleName='Obey Voice Events')
info['LSEnvironment'] = {'OBEY_VOICE_ENGINE': str(engine), 'OBEY_VOICE_EVENT_FIXTURE': str(fixture),
                       'OBEY_VOICE_LOG_DIR': str(fixture / 'logs'), 'OBEY_VOICE_TRACE_UI': '1', 'OBEY_VOICE_TOOL_PATH': str(fixture / 'bin') + ':/usr/bin:/bin:/opt/homebrew/bin:/usr/local/bin'}
with path.open('wb') as f:
    plistlib.dump(info, f)
subprocess.run(['codesign', '--force', '--deep', '--sign', '-', str(app)], check=True)
print(json.dumps({'app': str(app), 'fixture': str(fixture)}))
