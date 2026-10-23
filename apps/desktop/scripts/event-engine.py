#!/usr/bin/env python3
"""Protocol-only UI fixture: never loads user config or opens audio devices."""
import json
import os
from pathlib import Path
import sys
import threading
import time

root = Path(os.environ['OBEY_VOICE_EVENT_FIXTURE'])
assert root.is_absolute() and (root / '.fixture').is_file()
lock = threading.Lock()
def emit(value):
    with lock:
        print(json.dumps(value), flush=True)
def event(kind, data):
    emit({'event': kind, 'data': data})
def save(kind, text):
    with (root / 'actions.jsonl').open('a') as f:
        f.write(json.dumps({'kind': kind, 'text': text}) + '\n')

bundle = str(root / 'sample.meeting')
if '--bridge' not in sys.argv:
    args = sys.argv[1:]
    if args[:2] == ['meeting', 'list']:
        emit({'meetings': [{'description': 'Fixture meeting', 'started_at': 'Fixture time', 'bundle': bundle, 'id': 'sample.meeting'}]})
    elif args[:2] == ['meeting', 'show']:
        emit({'document': 'Fixture transcript: We should ship the recording workspace.'})
    elif args[:2] == ['meeting', 'route']:
        destination = args[args.index('--to') + 1]
        failed = destination == 'camp:Unavailable'
        if not failed:
            (root / 'routed-note.md').write_text('Fixture transcript and note delivered.\n')
        emit({'destination_id': destination, 'outcome': 'failed' if failed else 'routed',
              'detail': str(root / 'routed-note.md'), 'error': 'Fixture destination unavailable' if failed else ''})
    else:
        emit([] if args[:2] in [['persona', 'list'], ['config', 'schema']] else {})
    sys.exit(0)

event('ready', {'protocol': 1})
recording = threading.Event()
work_replay = os.environ.get('OBEY_VOICE_WORK_REPLAY') == '1'
workers = [
    {'ID': 'running-job', 'Task': 'Review desktop changes', 'State': 'running', 'Activity': 'Reading files'},
    {'ID': 'queued-job', 'Task': 'Check documentation', 'State': 'queued'},
]
def work_state():
    event('state', {'name': 'Fixture', 'session_id': 'work-replay',
                   'status': 'Ready for typed input', 'workers': workers,
                   'workers_enabled': False, 'recording': 'off'})
def stream():
    event('meeting_phase', 'listening')
    event('meeting_level', 0.45)
    event('meeting_partial', 'We should ship…')
    time.sleep(1)
    if recording.is_set():
        event('meeting_utterance', {'At': '00:04', 'Text': 'We should ship the recording workspace.'})
        event('meeting_partial', 'And make failures easy to…')

for line in sys.stdin:
    request = json.loads(line)
    method = request['method']
    save(method, request.get('text', ''))
    reply = {'id': request.get('id'), 'result': None}
    if method == 'shutdown':
        break
    if method == 'setup_status':
        reply['result'] = {'progress': {'version': 1, 'completed': True}, 'existing': True}
    elif method == 'sessions':
        reply['result'] = []
    elif method == 'start' and work_replay:
        reply['result'] = {'session_id': 'work-replay'}
        emit(reply)
        work_state()
        continue
    elif method == 'text' and work_replay:
        if request.get('text') == 'finish':
            for worker in workers:
                worker['State'] = 'succeeded'
        work_state()
    elif method == 'start':
        print('Fixture provider startup failed: model unavailable', file=sys.stderr, flush=True)
        reply['error'] = 'Fixture provider unavailable; inspect Diagnostics'
    elif method == 'meeting_start':
        recording.set()
        reply['result'] = {'path': bundle}
        emit(reply)
        threading.Thread(target=stream, daemon=True).start()
        continue
    elif method == 'meeting_pause':
        recording.clear()
    elif method == 'meeting_resume':
        recording.set()
        event('meeting_phase', 'listening')
    elif method == 'meeting_note' and request.get('text') == 'reject note':
        reply['error'] = 'Fixture note write failed'
    elif method == 'meeting_stop':
        recording.clear()
        reply['result'] = {'bundle': bundle}
        event('meeting_saved', reply['result'])
    emit(reply)
