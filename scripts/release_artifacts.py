#!/usr/bin/env python3
"""Package and verify the shared native payload used by distribution channels."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import tarfile
import tempfile


PLATFORMS = ("linux-amd64", "macos-arm64")
RELEASE_TAG = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?\Z")


def checked_version(value):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]*", value):
        raise ValueError(f"unsafe artifact version: {value!r}")
    return value


def version():
    if os.environ.get("GITHUB_REF_TYPE") == "tag":
        value = os.environ["GITHUB_REF_NAME"]
        if not RELEASE_TAG.fullmatch(value):
            raise ValueError(f"release tag must be vMAJOR.MINOR.PATCH[-PRERELEASE]: {value}")
        tag_commit = subprocess.check_output(["git", "rev-parse", f"{value}^{{commit}}"], text=True)
        head = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True)
        if tag_commit != head:
            raise ValueError("release tag does not identify the checked-out commit")
        return value
    return checked_version(subprocess.check_output(
        ["git", "describe", "--tags", "--match", "v[0-9]*", "--always", "--dirty"], text=True).strip())


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def archive_name(value, platform):
    checked_version(value)
    if platform not in PLATFORMS:
        raise ValueError(f"unsupported platform: {platform}")
    return f"obey-voice-{value}-{platform}.tar.gz"


def required_files(platform):
    suffix = "so" if platform == "linux-amd64" else "dylib"
    module = "linux" if platform == "linux-amd64" else "macos"
    return {"obey-voice", "LICENSE", "NOTICE", f"lib/libsherpa-onnx-c-api.{suffix}",
            f"lib/libonnxruntime.{suffix}", f"licenses/sherpa-onnx-go-{module}-LICENSE",
            "licenses/onnxruntime-LICENSE", "licenses/onnxruntime-ThirdPartyNotices.txt"}


def package(stage, platform, value, output):
    name = archive_name(value, platform)
    files = sorted(p for p in stage.rglob("*") if p.is_file() and p.name != "manifest.json")
    if any(p.is_symlink() for p in stage.rglob("*")):
        raise ValueError("native stage must contain regular files, not symlinks")
    relative = {p.relative_to(stage).as_posix() for p in files}
    missing = required_files(platform) - relative
    if missing:
        raise ValueError(f"missing native payload files: {sorted(missing)}")
    manifest = {"name": "obey-voice", "version": value, "platform": platform,
                "built_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                "files": [{"path": p.relative_to(stage).as_posix(), "sha256": digest(p)} for p in files]}
    (stage / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    output.mkdir(parents=True, exist_ok=True)
    archive = output / name
    with tarfile.open(archive, "w:gz", format=tarfile.PAX_FORMAT) as tar:
        tar.add(stage, arcname=f"obey-voice-{platform}")
    (output / (name + ".sha256")).write_text(f"{digest(archive)}  {name}\n")
    verify(archive, value, platform)
    # The Apple app's local vendor-engine workflow consumes the unversioned path.
    # Public releases use only the versioned archive and its checksum sidecar.
    shutil.copyfile(archive, stage.parent / f"obey-voice-{platform}.tar.gz")
    print(archive)


def verify(archive, value, platform):
    expected = archive_name(value, platform)
    if archive.name != expected:
        raise ValueError(f"expected archive name {expected}, got {archive.name}")
    checksum = archive.with_name(expected + ".sha256").read_text()
    if checksum != f"{digest(archive)}  {expected}\n":
        raise ValueError(f"archive checksum mismatch: {expected}")
    root = f"obey-voice-{platform}"
    with tarfile.open(archive, "r:gz") as tar:
        entries = {}
        seen = set()
        for member in tar.getmembers():
            path = PurePosixPath(member.name)
            if (path.is_absolute() or ".." in path.parts or not path.parts
                    or member.name != path.as_posix()
                    or path.parts[0] != root or member.name in seen
                    or not (member.isfile() or member.isdir())):
                raise ValueError(f"unsafe archive entry: {member.name}")
            seen.add(member.name)
            if member.isfile():
                entries[path.relative_to(root).as_posix()] = member
        if "manifest.json" not in entries:
            raise ValueError("missing manifest.json")
        manifest = json.load(tar.extractfile(entries["manifest.json"]))
        if (manifest.get("name"), manifest.get("version"), manifest.get("platform")) != ("obey-voice", value, platform):
            raise ValueError("manifest identity does not match the release")
        records = manifest["files"]
        declared = {record["path"] for record in records}
        if len(declared) != len(records) or declared != entries.keys() - {"manifest.json"}:
            raise ValueError("manifest file inventory does not match archive")
        if required_files(platform) - declared:
            raise ValueError("archive is missing required native payload files")
        if not entries["obey-voice"].mode & 0o111:
            raise ValueError("obey-voice is not executable")
        for record in records:
            stream = tar.extractfile(entries[record["path"]])
            if hashlib.file_digest(stream, "sha256").hexdigest() != record["sha256"]:
                raise ValueError(f"payload checksum mismatch: {record['path']}")


def smoke(archive, value, platform):
    verify(archive, value, platform)
    with tempfile.TemporaryDirectory(prefix="obey-voice-release-") as scratch:
        with tarfile.open(archive, "r:gz") as tar:
            tar.extractall(scratch, filter="data")
        root = Path(scratch) / f"obey-voice-{platform}"
        env = {key: val for key, val in os.environ.items()
               if not key.startswith(("DYLD_", "LD_", "OBEY_VOICE_", "SAMANTHA_"))}
        env.update(HOME=scratch, XDG_CONFIG_HOME=scratch, XDG_CACHE_HOME=scratch, NO_COLOR="1")
        result = subprocess.run([str(root / "obey-voice"), "--version"], cwd=scratch,
                                env=env, check=True, text=True, capture_output=True, timeout=30)
        if value not in result.stdout.split():
            raise ValueError(f"CLI version does not match {value}: {result.stdout!r}")
        print(result.stdout.strip())
        # Prove the executable depends on the bundled library, not the module cache.
        suffix = "so" if platform == "linux-amd64" else "dylib"
        for name in (f"libsherpa-onnx-c-api.{suffix}", f"libonnxruntime.{suffix}"):
            library = root / "lib" / name
            hidden = library.with_suffix(".missing")
            library.rename(hidden)
            try:
                missing = subprocess.run([str(root / "obey-voice"), "--version"], cwd=scratch,
                                         env=env, capture_output=True, timeout=30)
                if missing.returncode == 0 or name.encode() not in missing.stderr:
                    raise ValueError(f"expected loader failure without bundled {name}; check dependency paths")
            finally:
                hidden.rename(library)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("version")
    for command in ("package", "verify", "smoke"):
        sub = commands.add_parser(command)
        sub.add_argument("path", type=Path)
        sub.add_argument("platform", choices=PLATFORMS)
        sub.add_argument("version")
        if command == "package":
            sub.add_argument("--output", type=Path, default=Path("dist/release"))
    args = parser.parse_args()
    if args.command == "version":
        print(version())
    elif args.command == "package":
        package(args.path, args.platform, args.version, args.output)
    else:
        globals()[args.command](args.path, args.version, args.platform)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, tarfile.TarError, subprocess.SubprocessError) as error:
        raise SystemExit(f"release artifact: {error}") from error
