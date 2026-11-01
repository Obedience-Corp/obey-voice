#!/usr/bin/env python3
"""Quiet, explicit finite-worker lifecycle proof; no capture, playback or downloads."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time
import wave


def digest(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def group_members(pgid):
    listing = subprocess.check_output(['ps', '-axo', 'pid=,pgid=,comm='], text=True)
    members = []
    for line in listing.splitlines():
        parts = line.strip().split(None, 2)
        if len(parts) == 3 and parts[1] == str(pgid):
            members.append({'pid': int(parts[0]), 'command': parts[2]})
    return members


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--worker', type=Path, required=True)
    ap.add_argument('--config', type=Path, required=True)
    ap.add_argument('--request', type=Path, required=True)
    ap.add_argument('--output', type=Path, required=True)
    ap.add_argument('--sandbox-profile', type=Path)
    args = ap.parse_args()
    root = args.output.resolve()
    root.mkdir(mode=0o700)  # A fresh explicit directory; never replace user data.
    config = json.loads(args.config.read_text())
    request = json.loads(args.request.read_text())
    source_hash = digest(request['input']['path'])
    results = []
    for mode in ['complete', 'cancel', 'forced']:
        attempt = root / mode
        attempt.mkdir(mode=0o700)
        cfg = dict(config, storage_root=str(attempt))
        req = dict(request, job_id=mode, output_dir=str(attempt / 'artifact'))
        if mode != 'complete' and request['protocol'] == 'obey-audio-job/v1':
            req['script'] = ('This recording is a quiet cancellation fixture. ' * 15).strip()
        cfg_path, req_path = attempt / 'config.json', attempt / 'request.json'
        cfg_path.write_text(json.dumps(cfg, indent=2))
        req_path.write_text(json.dumps(req, indent=2))
        command = [str(args.worker.resolve()), 'transcribe', 'audio-job',
                   '--audio-config', str(cfg_path), '--request', str(req_path), '--jsonl']
        if args.sandbox_profile:
            command = ['sandbox-exec', '-f', str(args.sandbox_profile.resolve())] + command
        events_path, errors_path = attempt / 'events.jsonl', attempt / 'stderr.log'
        started = time.monotonic()
        members, stop_time = [], None
        with events_path.open('w') as out, errors_path.open('w') as err:
            proc = subprocess.Popen(command, stdout=out, stderr=err, start_new_session=True)
            try:
                if mode != 'complete':
                    deadline = started + 100
                    while proc.poll() is None and time.monotonic() < deadline:
                        if '"type":"accepted"' in events_path.read_text():
                            members = group_members(proc.pid)
                            assert any('llama-server' in member['command'] for member in members), members
                            stop_time = time.monotonic()
                            if mode == 'forced':
                                os.killpg(proc.pid, signal.SIGKILL)
                            else:
                                proc.send_signal(signal.SIGTERM)
                            break
                        time.sleep(.02)
                    assert stop_time is not None, errors_path.read_text()
                code = proc.wait(timeout=120 if mode == 'complete' else 5)
                reap_deadline = time.monotonic() + 2
                while group_members(proc.pid) and time.monotonic() < reap_deadline:
                    time.sleep(.02)
                assert not group_members(proc.pid), 'Owned children survived worker exit'
            finally:
                # These are exclusively children in the new group created above.
                if group_members(proc.pid):
                    try:
                        os.killpg(proc.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                proc.wait(timeout=5)
        remaining = group_members(proc.pid)
        assert not remaining, remaining
        events = [json.loads(line) for line in events_path.read_text().splitlines() if line]
        record = {'case': mode, 'exit': code, 'seconds': time.monotonic() - started,
                  'events': [event['type'] for event in events], 'owned_group_before_stop': members,
                  'owned_processes_after_exit': remaining,
                  'stop_seconds': time.monotonic() - stop_time if stop_time else None}
        if mode == 'complete':
            assert code == 0 and record['events'][-1] == 'completed', errors_path.read_text()
            manifest = json.loads((attempt / 'artifact' / 'manifest.json').read_text())
            output = attempt / 'artifact' / manifest['output']['path']
            assert digest(output) == manifest['output']['sha256']
            if request['protocol'] == 'obey-audio-job/v2':
                assert manifest['output']['kind'] == 'json'
                assert output.stat().st_size == manifest['output']['bytes']
                findings = json.loads(output.read_text())
                assert findings['schema'] == 'obey-audio-findings/v1'
                assert findings['task'] == request['task']
                with wave.open(request['input']['path'], 'rb') as stream:
                    pcm = stream.readframes(stream.getnframes())
                origin = request['input']['source_origin_samples']
                count = request['input']['sample_count']
                windows = findings['windows']
                assert windows[0]['start_sample'] == origin
                assert windows[-1]['end_sample'] == origin + count
                for index, window in enumerate(windows):
                    start, end = window['start_sample'] - origin, window['end_sample'] - origin
                    assert 0 <= start < end <= count and end - start <= 30 * 16000
                    if index:
                        assert window['start_sample'] == windows[index - 1]['end_sample'] - 2 * 16000
                    expected = io.BytesIO()
                    with wave.open(expected, 'wb') as segment:
                        segment.setnchannels(1)
                        segment.setsampwidth(2)
                        segment.setframerate(16000)
                        segment.writeframes(pcm[start * 2:end * 2])
                    assert hashlib.sha256(expected.getvalue()).hexdigest() == window['input_window_sha256']
                    assert window['text'].strip()
                record.update(output_sha256=digest(output), findings=findings,
                              full_input_samples=count, windows=len(windows))
            else:
                with wave.open(str(output), 'rb') as stream:
                    assert stream.getframerate() == 24000 and stream.getnchannels() == 1
                    assert stream.getnframes() == manifest['output']['sample_count']
                    record.update(output_sha256=digest(output), samples=stream.getnframes(),
                                  duration_seconds=stream.getnframes() / 24000)
            record['manifest'] = manifest
        else:
            assert code != 0 and not (attempt / 'artifact').exists()
            if mode == 'cancel':
                assert record['events'][-1] == 'cancelled', record
        leftovers = [path for path in attempt.iterdir()
                     if path.name.startswith('.audio-') or path.name.endswith('.lock')]
        record['incomplete_files_after_worker_exit'] = [path.name for path in leftovers]
        if mode != 'forced':
            assert not leftovers, leftovers
        # A killed worker cannot run defer. The owning caller retires its scratch
        # only after reaping the whole group, as the editor's finite client does.
        for path in leftovers:
            shutil.rmtree(path) if path.is_dir() else path.unlink()
        record['caller_retired_incomplete_files'] = True
        results.append(record)
    assert digest(request['input']['path']) == source_hash
    receipt = {'worker_sha256': digest(args.worker), 'source_unchanged': True,
               'external_network_denied': bool(args.sandbox_profile),
               'loopback_allowed_for_owned_runtime': True, 'playback': False,
               'listening_or_task_quality_qualified': False, 'results': results}
    (root / 'receipt.json').write_text(json.dumps(receipt, indent=2))
    print(json.dumps({'receipt': str(root / 'receipt.json'),
                      'cases': [r['case'] for r in results], 'passed': True}))


if __name__ == '__main__':
    main()
