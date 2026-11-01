#!/usr/bin/env python3
"""File-only native waveform success/cancellation proof; no quality claim.

Runs each worker under an explicit deny-network sandbox. Uses an explicit installed pack and licensed
source/reference WAVs; never accesses devices or assistant settings.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import time
import wave


def sha(path):
    with Path(path).open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def write(path, data):
    path.write_text(json.dumps(data, indent=2) + '\n')


def children(pid):
    rows = subprocess.check_output(['ps', '-axo', 'pid=,ppid=,comm='], text=True)
    return [int(p) for row in rows.splitlines() for p, parent, command in [row.split(None, 2)]
            if int(parent) == pid and Path(command).name == 'keepshot-voice-worker']


def alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ('worker', 'config', 'request', 'out', 'sandbox-profile'):
        parser.add_argument('--' + flag, type=Path, required=True)
    args = parser.parse_args()
    worker = args.worker.resolve(strict=True)
    root = args.out.resolve()
    root.mkdir(parents=True, exist_ok=False)
    config = json.loads(args.config.read_text())
    config['storage_root'] = str(root)
    config_path = root / 'config.json'
    write(config_path, config)
    source = json.loads(args.request.read_text())
    assert source['protocol'] == 'obey-audio-job/v4' and source['task'] == 'voice_conversion'
    assert source['input']['sample_count'] > 22050 * 10, 'Use a multi-window human recording'
    prefix = ['sandbox-exec', '-f', str(args.sandbox_profile.resolve(strict=True)), str(worker), 'transcribe']
    caps = subprocess.run(prefix + ['audio-capabilities', '--audio-config', str(config_path), '--json'], capture_output=True, text=True, timeout=30, check=True)
    (root / 'capabilities.json').write_text(caps.stdout)
    task = json.loads(caps.stdout)['tasks'][0]
    assert task['task'] == 'voice_conversion' and task['supported'] and task['installed'] and not task['qualified']
    receipts = []
    for name in ('success', 'cancel'):
        request = dict(source, job_id=name, output_dir=str(root / name), runtime=config['runtime'])
        request_path = root / (name + '-request.json')
        write(request_path, request)
        started = time.monotonic()
        child_pids = []
        partial_bytes = 0
        with (root / (name + '-events.jsonl')).open('w') as events, (root / (name + '-stderr.log')).open('w') as errors:
            process = subprocess.Popen(prefix + ['audio-job', '--audio-config', str(config_path), '--request', str(request_path), '--jsonl'], stdout=events, stderr=errors, start_new_session=True)
            try:
                if name == 'cancel':
                    deadline = time.monotonic() + 20
                    while process.poll() is None and time.monotonic() < deadline:
                        child_pids = children(process.pid)
                        partials = list(root.glob('.audio-job-*/audio.wav.partial'))
                        partial_bytes = max((p.stat().st_size for p in partials if p.exists()), default=0)
                        if child_pids and partial_bytes > 44:
                            break
                        time.sleep(.05)
                    assert child_pids and partial_bytes > 44, 'No live native worker with converted PCM observed before cancellation'
                    cancelled_at = time.monotonic()
                    process.send_signal(signal.SIGINT)
                code = process.wait(timeout=10 if name == 'cancel' else request['limits']['timeout_seconds'] + 10)
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                # Cleanup only exact observed children; a failure remains a failure.
                leaked = [pid for pid in child_pids if alive(pid)]
                for pid in leaked:
                    os.killpg(pid, signal.SIGKILL)
                assert not leaked, f'Owned native child survived facade exit: {leaked}'
        events = [json.loads(line) for line in (root / (name + '-events.jsonl')).read_text().splitlines()]
        expected = ['accepted', 'progress', 'artifact', 'completed'] if name == 'success' else ['accepted', 'progress', 'cancelled']
        assert [e['type'] for e in events] == expected, events
        assert all(e['seq'] == i and e['job_id'] == name and e['protocol'] == request['protocol'] for i, e in enumerate(events))
        receipt = {'job': name, 'elapsed_seconds': time.monotonic() - started, 'exit_code': code, 'events': expected, 'request_sha256': sha(request_path)}
        if name == 'success':
            assert code == 0
            folder = root / name
            manifest = json.loads((folder / 'manifest.json').read_text())
            assert set(p.name for p in folder.iterdir()) == {'manifest.json', 'audio.wav'}
            for key in ('source', 'input', 'reference', 'runtime', 'duration_policy', 'language'):
                assert manifest[key] == request[key], key
            assert manifest['transport'] == 'waveform_conversion'
            assert manifest['chunk_policy'] == '10s-core-1s-context-linear-crossfade/v1'
            assert manifest['conversion_sampling']['temperature'] == 0
            for key, asset in [('model_digest', 'converter'), ('encoder_digest', 'encoder'), ('runtime_digest', 'runtime_library'), ('worker_digest', 'executable')]:
                assert manifest[key] == config['waveform'][asset]['sha256']
            output = folder / manifest['output']['path']
            assert sha(output) == manifest['output']['sha256']
            with wave.open(str(output)) as wav:
                assert (wav.getnchannels(), wav.getsampwidth(), wav.getframerate(), wav.getnframes()) == (1, 2, 22050, request['input']['sample_count'])
            cursor = request['input']['source_origin_samples']
            for window in manifest['source_windows']:
                assert window['start_sample'] == cursor and 0 < window['end_sample'] - cursor <= 220500
                cursor = window['end_sample']
            assert cursor == request['input']['source_origin_samples'] + request['input']['sample_count']
            receipt['manifest'] = manifest
            receipt['manifest_sha256'] = sha(folder / 'manifest.json')
        else:
            assert code != 0 and not (root / name).exists()
            receipt.update(cancel_to_exit_seconds=time.monotonic() - cancelled_at, observed_native_child=True, converted_partial_bytes=partial_bytes, child_reaped=True)
        assert not list(root.glob('.audio-job-*')) and not list(root.glob('*.lock')) and not list(root.glob('.waveform-version-*'))
        receipts.append(receipt)
    write(root / 'receipt.json', {'schema': 'waveform-transport-proof/v1', 'worker_sha256': sha(worker), 'native_worker_sha256': sha(config['waveform']['executable']['path']), 'quality_qualified': False, 'receipts': receipts})
    print(json.dumps({'passed': True, 'receipt': str(root / 'receipt.json')}))


if __name__ == '__main__':
    main()
