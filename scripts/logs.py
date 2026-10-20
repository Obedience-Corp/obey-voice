#!/usr/bin/env python3
"""Read or follow local desktop diagnostics, including newly started app runs."""
import argparse
import os
from pathlib import Path
import sys
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--follow', action='store_true')
parser.add_argument('--demo', action='store_true')
args = parser.parse_args()
if sys.platform == 'darwin':
    base = Path.home() / 'Library/Logs'
elif sys.platform == 'win32':
    base = Path(os.environ.get('LOCALAPPDATA', str(Path.home())))
else:
    base = Path(os.environ.get('XDG_STATE_HOME', str(Path.home() / '.local/state')))
folder = Path(os.environ.get('OBEY_VOICE_LOG_DIR', base / ('obey-voice-agent-demo' if args.demo else 'obey-voice-agent')))
print(f'Logs: {folder}', file=sys.stderr)
current = None
reader = None
try:
    while True:
        files = sorted(folder.glob('run-*.jsonl'))
        files = [p for p in files if not p.name.endswith('.previous.jsonl')]
        latest = files[-1] if files else None
        if latest != current:
            if reader:
                reader.close()
            current = latest
            reader = current.open() if current else None
            if current:
                print(f'Run: {current.name}', file=sys.stderr)
        if reader:
            for line in reader:
                print(line, end='', flush=True)
            # Rotation retains the same path with a new inode.
            if current.stat().st_ino != os.fstat(reader.fileno()).st_ino:
                reader.close()
                reader = current.open()
        if not args.follow:
            break
        time.sleep(0.5)
except KeyboardInterrupt:
    pass
finally:
    if reader:
        reader.close()
