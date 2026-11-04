#!/usr/bin/env python3
"""Regenerate documented synthetic fixtures with explicit, existing model assets."""

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import wave


def digest(path):
    if path.is_dir():
        tree = hashlib.sha256()
        for child in sorted(path.rglob('*')):
            if child.is_file():
                tree.update(child.relative_to(path).as_posix().encode() + b'\0')
                tree.update(hashlib.sha256(child.read_bytes()).digest())
        return tree.hexdigest()
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--models-dir', type=Path, required=True)
    parser.add_argument('--verify-only', action='store_true')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    manifest = root / 'docs/audio/public-fixtures.json'
    spec = json.loads(manifest.read_text())
    models = args.models_dir.resolve()
    for filename, field in [('model.onnx', 'model_sha256'), ('voices.bin', 'voices_sha256')]:
        if digest(models / filename) != spec[field]:
            raise SystemExit(f'{filename} differs from the recorded source pack')
    # Golden-TTS prefers a converted English pack when present. Do not silently
    # synthesize with different assets from those whose hashes were checked.
    if (models / 'kokoro-v1.0-en' / '.kokoro-v1-en-source').exists():
        raise SystemExit('use a standalone multi-lang pack directory without an English-pack override')
    for filename, expected in spec['auxiliary_sha256'].items():
        if digest(models / filename) != expected:
            raise SystemExit(f'{filename} differs from the recorded source pack')
    if args.verify_only:
        print('Model, voices and auxiliary synthesis inputs verified.')
        return
    with tempfile.TemporaryDirectory(prefix='obey-public-audio-') as temp:
        temp = Path(temp)
        binary = temp / 'golden-tts'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/golden-tts'], cwd=root, check=True)
        for index, entry in enumerate(spec['files']):
            raw = temp / f'{index}.wav'
            metadata = temp / f'{index}.json'
            subprocess.run([str(binary), '-models-dir', str(models), '-voice', entry['voice'],
                            '-speed', str(spec['speed']), '-text', entry['text'],
                            '-out', str(raw), '-meta', str(metadata)], cwd=root, check=True)
            generated = json.loads(metadata.read_text())
            if (generated['prepared_text'] != entry['text']
                    or generated['model_sha256'] != spec['model_sha256']
                    or generated['voices_sha256'] != spec['voices_sha256']):
                raise SystemExit(f'unexpected synthesis provenance: {entry["path"]}')
            target = root / entry['path']
            subprocess.run(['ffmpeg', '-y', '-loglevel', 'error', '-i', str(raw),
                            '-ar', str(spec['sample_rate']), '-ac', '1', '-c:a', 'pcm_s16le',
                            '-map_metadata', '-1', str(target)], check=True)
            with wave.open(str(target), 'rb') as audio:
                entry['samples'] = audio.getnframes()
                if (audio.getframerate(), audio.getnchannels(), audio.getsampwidth()) != (16000, 1, 2):
                    raise SystemExit(f'unexpected PCM format: {entry["path"]}')
                pcm = audio.readframes(audio.getnframes())
            # Match the application's canonical WAV encoder: no ffmpeg LIST or
            # other ancillary chunks. Setup forwards these exact bytes to ASR.
            with wave.open(str(target), 'wb') as audio:
                audio.setparams((1, 2, 16000, 0, 'NONE', 'not compressed'))
                audio.writeframes(pcm)
            entry['pcm_sha256'] = hashlib.sha256(pcm).hexdigest()
            entry['sha256'] = digest(target)
    manifest.write_text(json.dumps(spec, indent=2) + '\n')
    print('Generated fixtures; review new bytes and refresh media-review.json separately.')


if __name__ == '__main__':
    main()
