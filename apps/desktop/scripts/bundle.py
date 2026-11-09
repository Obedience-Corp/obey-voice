#!/usr/bin/env python3
"""Package locally built binaries as a macOS application (no publishing)."""
import argparse
import json
import os
import pathlib
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import sys

parser = argparse.ArgumentParser()
parser.add_argument('--release', action='store_true')
parser.add_argument('--demo', action='store_true')
args = parser.parse_args()
version = os.environ.get('OBEY_VOICE_DESKTOP_VERSION', '0.1.0').removeprefix('v')
if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
    parser.error('OBEY_VOICE_DESKTOP_VERSION must be a stable semantic version')
if sys.platform != 'darwin':
    parser.error('macOS app bundles must be built on macOS')
root = pathlib.Path(__file__).resolve().parent.parent
metadata = json.loads(subprocess.check_output(
    ['cargo', 'metadata', '--locked', '--no-deps', '--format-version', '1'],
    cwd=root, text=True))
target_dir = Path(metadata['target_directory'])
bundle = root / 'dist' / ('Obey Voice Demo.app' if args.demo else 'Obey Voice Agent.app')
if bundle.exists():
    shutil.rmtree(bundle)
macos = bundle / 'Contents' / 'MacOS'
macos.mkdir(parents=True, exist_ok=True)
resources = bundle / 'Contents' / 'Resources'
resources.mkdir(exist_ok=True)
subprocess.run([
    'iconutil', '--convert', 'icns',
    '--output', str(resources / 'AppIcon.icns'),
    str(root / 'assets' / 'AppIcon.iconset'),
], check=True)
for source, name in [(target_dir / ('release' if args.release else 'debug') / 'obey-voice-agent', 'obey-voice-agent'), (root / 'bin' / 'obey-voice-engine', 'obey-voice-engine')]:
    shutil.copy2(source, macos / name)
# Include the runtime's native speech libraries and replace developer-machine
# rpaths with the app's Frameworks directory.
frameworks = bundle / 'Contents' / 'Frameworks'
frameworks.mkdir(exist_ok=True)
engine = macos / 'obey-voice-engine'
load_commands = subprocess.check_output(['otool', '-l', str(engine)], text=True).splitlines()
rpaths = []
for i, line in enumerate(load_commands):
    if line.strip() == 'cmd LC_RPATH':
        rpaths.append(load_commands[i + 2].strip().removeprefix('path ').split(' (offset')[0])
queue = [engine]
seen = set()
while queue:
    binary = queue.pop()
    for line in subprocess.check_output(['otool', '-L', str(binary)], text=True).splitlines()[1:]:
        dependency = line.strip().split(' (compatibility')[0]
        if dependency.startswith(('/System/', '/usr/lib/')):
            continue
        name = Path(dependency).name
        if name == binary.name or name in seen:
            continue
        source = next((Path(base) / name for base in rpaths if (Path(base) / name).is_file()), None)
        if source is None and Path(dependency).is_file():
            source = Path(dependency)
        if source is None:
            raise RuntimeError(f'Cannot bundle native dependency: {dependency}')
        target = frameworks / name
        shutil.copy2(source, target)
        target.chmod(0o755)
        subprocess.run(['install_name_tool', '-id', '@rpath/' + name, str(target)], check=True)
        seen.add(name)
        queue.append(target)
for path in rpaths:
    subprocess.run(['install_name_tool', '-delete_rpath', path, str(engine)], check=True)
subprocess.run(['install_name_tool', '-add_rpath', '@executable_path/../Frameworks', str(engine)], check=True)
info = dict(CFBundleName='Obey Voice Demo' if args.demo else 'Obey Voice Agent', CFBundleDisplayName='Obey Voice Agent', CFBundleExecutable='obey-voice-agent', CFBundleIdentifier='com.obedience.voice-agent.demo' if args.demo else 'com.obedience.voice-agent', CFBundleVersion='1', CFBundleShortVersionString='0.1.0', CFBundlePackageType='APPL', LSMinimumSystemVersion='13.0', NSMicrophoneUsageDescription='Obey Voice uses your microphone for conversations, voice enrollment, and meeting recordings.', NSHighResolutionCapable=True)
info.update(CFBundleVersion=version, CFBundleShortVersionString=version,
            LSMinimumSystemVersion=os.environ.get('MACOSX_DEPLOYMENT_TARGET', '13.0'))
if args.demo:
    info['LSEnvironment'] = {'OBEY_VOICE_AGENT_DEMO': '1'}
info['CFBundleIconFile'] = 'AppIcon.icns'
with (bundle / 'Contents' / 'Info.plist').open('wb') as f:
    plistlib.dump(info, f)
subprocess.run(['codesign', '--force', '--deep', '--sign', '-', str(bundle)], check=True)
print(bundle)
