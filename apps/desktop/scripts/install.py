#!/usr/bin/env python3
"""Install the locally signed macOS bundle without launching it."""
import argparse
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys
import tempfile


def check_bundle(bundle):
    if bundle.is_symlink() or not bundle.is_dir():
        raise ValueError(f'Expected an app directory: {bundle}')
    with (bundle / 'Contents/Info.plist').open('rb') as stream:
        info = plistlib.load(stream)
    if info.get('CFBundleIdentifier') != 'com.obedience.voice-agent':
        raise ValueError(f'Refusing to replace an unrelated app: {bundle}')


def verify(bundle):
    subprocess.run(['codesign', '--verify', '--deep', '--strict', str(bundle)], check=True)


def install(directory):
    source = Path(__file__).resolve().parent.parent / 'dist/Obey Voice Agent.app'
    check_bundle(source)
    verify(source)
    directory = directory.expanduser().resolve()
    destination = directory / source.name
    if destination == source:
        raise ValueError('Choose an install directory outside the build output')
    if destination.exists() or destination.is_symlink():
        check_bundle(destination)
    directory.mkdir(parents=True, exist_ok=True)
    # Stage and verify beside the destination before replacing a prior install.
    with tempfile.TemporaryDirectory(prefix='.obey-voice-install-', dir=directory) as temporary:
        staged = Path(temporary) / source.name
        # Keep rollback data outside automatic cleanup if restoring ever fails.
        previous = directory / (Path(temporary).name + '.previous.app')
        shutil.copytree(source, staged, symlinks=True)
        verify(staged)
        had_previous = destination.exists()
        if had_previous:
            destination.rename(previous)
        try:
            staged.rename(destination)
            verify(destination)
        except Exception:
            if destination.exists():
                destination.rename(staged)
            if had_previous:
                previous.rename(destination)
            raise
        if had_previous:
            shutil.rmtree(previous)
    print(f'Installed {destination}')
    print('Open the app from Finder or run:')
    print(f'  open "{destination}"')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, default=Path('~/Applications'))
    args = parser.parse_args()
    if sys.platform != 'darwin':
        parser.error('macOS app installation requires macOS')
    try:
        install(args.directory)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, f'Install failed: {error}\n')


if __name__ == '__main__':
    main()
