#!/usr/bin/env python3
"""Build and install this checkout with its native libraries; no release needed."""

import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile

import release_artifacts as artifacts


def native_platform():
    target = {("Darwin", "arm64"): "macos-arm64",
              ("Linux", "x86_64"): "linux-amd64"}.get(
                  (platform.system(), platform.machine()))
    if target is None:
        raise ValueError("dev install supports Apple Silicon macOS and x86_64 Linux")
    return target


def go_bin():
    configured = subprocess.check_output(["go", "env", "GOBIN"], text=True).strip()
    if configured:
        return Path(configured)
    gopath = subprocess.check_output(["go", "env", "GOPATH"], text=True).strip()
    return Path(gopath.split(os.pathsep)[0]) / "bin"


def install(archive, value, target, destination):
    """Verify and stage before atomically replacing the executable entry point."""
    artifacts.verify(archive, value, target)
    destination = destination.absolute()
    destination.mkdir(parents=True, exist_ok=True)
    entry = destination / "obey-voice"
    alias = destination / "samantha"
    for path in (entry, alias):
        if path.is_dir() and not path.is_symlink():
            raise ValueError(f"install entry is a directory: {path}")
    store = destination / ".obey-voice-dev"
    store.mkdir(exist_ok=True)
    previous = entry.resolve() if entry.is_symlink() else None
    staging = Path(tempfile.mkdtemp(prefix="install-", dir=store))
    activated = False
    try:
        with tarfile.open(archive, "r:gz") as tar:
            tar.extractall(staging, filter="data")
        executable = staging / f"obey-voice-{target}" / "obey-voice"
        # Exercise the actual installed path without models, personal settings,
        # or loader overrides before redirecting the user's command to it.
        with tempfile.TemporaryDirectory(prefix="obey-voice-install-check-") as scratch:
            env = {key: val for key, val in os.environ.items()
                   if not key.startswith(("DYLD_", "LD_", "OBEY_VOICE_", "SAMANTHA_"))}
            env.update(HOME=scratch, XDG_CONFIG_HOME=scratch, XDG_CACHE_HOME=scratch)
            result = subprocess.run([str(executable), "--version"], env=env, cwd=scratch,
                                    check=True, capture_output=True, text=True, timeout=30)
            if value not in result.stdout.split():
                raise ValueError(f"installed version does not match {value}: {result.stdout!r}")
        link = staging / "entry"
        link.symlink_to(executable)
        # os.replace replaces a symlink itself; cp would overwrite its release
        # or Homebrew target and leave the wrong sibling libraries in place.
        os.replace(link, entry)
        activated = True
        link.symlink_to(entry)
        os.replace(link, alias)
        print(f"Installed checkout {value}: {entry}")
        # Only retire the payload previously owned by this installer. Never
        # delete or overwrite the target of an existing release/Homebrew link.
        if previous is not None:
            old = previous.parent.parent
            if (old.parent == store.resolve() and old.name.startswith("install-")
                    and previous.name == "obey-voice"
                    and previous.parent.name == f"obey-voice-{target}"):
                try:
                    shutil.rmtree(old)
                except OSError as error:
                    print(f"Could not remove previous dev payload {old}: {error}")
        return entry
    finally:
        if not activated:
            shutil.rmtree(staging)


def main():
    target = native_platform()
    destination = go_bin()
    # These recipes only build/package locally. They never create tags, publish,
    # fetch released binaries, or invoke the release coordinator.
    subprocess.run(["just", "release", target.split("-")[0] + "-artifact"], check=True)
    value = artifacts.version()
    archive = Path("dist/release") / artifacts.archive_name(value, target)
    entry = install(archive, value, target, destination)
    if os.environ.get("OBEY_VOICE_SKIP_MODELS_ENSURE") == "1":
        print("Skipping model download. Run: obey-voice models ensure")
    else:
        print("Downloading default model assets (STT/TTS/VAD for this config)...", flush=True)
        result = subprocess.run([str(entry), "models", "ensure"])
        if result.returncode:
            print("Binary installed; voice setup is incomplete. Resolve the model/runtime "
                  "error above, then run: obey-voice models ensure")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, tarfile.TarError, subprocess.SubprocessError) as error:
        raise SystemExit(f"dev install: {error}") from error
