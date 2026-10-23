#!/usr/bin/env python3
"""Deterministic onboarding replay: fake downloads/audio/provider, isolated state only."""
import json
import os
from pathlib import Path
import sys
import time

root = Path(os.environ['OBEY_VOICE_EVENT_FIXTURE'])
assert root.is_absolute() and (root / '.fixture').is_file()

def emit(value):
    print(json.dumps(value), flush=True)

def event(data):
    emit({'event': 'state', 'data': data})

progress_file = root / 'desktop-setup.json'
progress = {'version': 1, 'step': 2, 'completed': False, 'deferred': False,
            'text_only': False, 'silent': False}

if '--bridge' not in sys.argv:
    args = sys.argv[1:]
    if args[:2] == ['models', 'status']:
        emit([{'id': 'fixture.speech', 'name': 'Fixture speech model',
               'installed': (root / 'installed').exists()}])
    elif args[:2] == ['models', 'ensure']:
        attempt_file = root / 'attempt'
        attempt = int(attempt_file.read_text()) + 1 if attempt_file.exists() else 1
        attempt_file.write_text(str(attempt))
        emit({'asset': 'fixture.speech', 'phase': 'download', 'bytes': 25000000, 'total': 100000000, 'pct': 25})
        if attempt == 1:
            emit({'done': True, 'error': 'Fixture download interrupted; check your connection and retry.', 'asset': 'fixture.speech'})
            sys.exit(1)
        if attempt == 2:
            time.sleep(120)  # Deliberately waits for the UI's cancellation signal.
        (root / 'installed').touch()
        emit({'asset': 'fixture.speech', 'phase': 'done', 'bytes': 100000000, 'total': 100000000, 'pct': 100})
        emit({'done': True, 'installed': ['fixture.speech']})
    elif args[:2] == ['config', 'schema']:
        emit({'keys': []})
    elif args[:2] == ['config', 'get']:
        emit({'values': {}, 'persona': None})
    else:
        emit([])
    sys.exit(0)

state = {'name': 'Fixture Obey', 'session_id': 'setup-replay', 'voice_available': True,
         'mic': True, 'muted': False, 'input_level': 0.55, 'busy': True, 'turns': [], 'error': False}
turn_attempt = 0
emit({'event': 'ready', 'data': {'protocol': 1}})
for line in sys.stdin:
    request = json.loads(line)
    method = request['method']
    with (root / 'actions.jsonl').open('a') as f:
        f.write(json.dumps(request) + '\n')
    reply = {'id': request.get('id'), 'result': None}
    if method == 'shutdown':
        break
    if method == 'setup_status':
        if progress_file.exists():
            progress = json.loads(progress_file.read_text())
        reply['result'] = {'progress': progress, 'existing': True, 'config_path': str(root / 'config.yaml')}
    elif method == 'setup_save':
        progress = request['config']
        progress_file.write_text(json.dumps(progress))
        reply['result'] = progress
    elif method == 'setup_provider':
        reply['result'] = {'name': 'brain-provider', 'severity': 'ok', 'detail': 'Replay provider available'}
    elif method == 'sessions':
        reply['result'] = []
    elif method == 'start':
        state.update(mic=True, busy=True, error=False, turns=[])
        emit(reply)
        event(state)
        continue
    elif method == 'mic':
        state.update(mic=request['enabled'], busy=request['enabled'])
    elif method == 'text':
        turn_attempt += 1
        if turn_attempt == 1:
            state.update(error=True, status='Fixture provider account unavailable. Sign in and retry.', busy=False)
        else:
            state.update(error=False, status='Reply finished', busy=False,
                         turns=[{'role': 'assistant', 'content': 'Your setup test worked.'}])
    elif method == 'stop':
        state.update(mic=False, busy=False)
    emit(reply)
    if method in ['mic', 'text', 'stop']:
        event(state)
