#!/usr/bin/env python3
"""Publish a complete native release through a verified draft, using gh."""

import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from release_artifacts import PLATFORMS, RELEASE_TAG, archive_name, digest, verify


def gh(*args):
    return subprocess.check_output(["gh", *args], text=True, stderr=subprocess.PIPE)


def release_state(tag):
    try:
        return json.loads(gh("release", "view", tag, "--json", "tagName,isDraft,assets"))
    except subprocess.CalledProcessError as error:
        if "release not found" in error.stderr.lower():
            return None
        raise


def validate_set(directory, tag):
    if not RELEASE_TAG.fullmatch(tag):
        raise ValueError("publication requires vMAJOR.MINOR.PATCH[-PRERELEASE]")
    files = []
    for platform in PLATFORMS:
        archive = directory / archive_name(tag, platform)
        verify(archive, tag, platform)
        files.extend((archive, archive.with_name(archive.name + ".sha256")))
    return files


def download_and_verify(tag, files):
    with tempfile.TemporaryDirectory(prefix="obey-voice-readback-") as scratch:
        args = ["release", "download", tag, "--dir", scratch]
        for file in files:
            args.extend(("--pattern", file.name))
        gh(*args)
        validate_set(Path(scratch), tag)
        for file in files:
            if digest(Path(scratch) / file.name) != digest(file):
                raise ValueError(f"release readback differs from validated input: {file.name}")


def publish(directory, tag):
    files = validate_set(directory, tag)
    state = release_state(tag)
    if state and not state["isDraft"]:
        # A successful retry is read-only. Never clobber published assets.
        download_and_verify(tag, files)
        print(f"{tag} already published with these assets; unchanged")
        return
    if state is None:
        gh("release", "create", tag, "--draft", "--verify-tag", "--title",
           f"Obey Voice {tag}", "--generate-notes")
    # Draft assets may be replaced when a failed attempt is retried. Both
    # platform jobs have already succeeded before this script is invoked.
    gh("release", "upload", tag, *(str(file) for file in files), "--clobber")
    download_and_verify(tag, files)
    args = ["release", "edit", tag, "--draft=false", "--latest=false"]
    if "-" in tag:
        args.append("--prerelease")
    gh(*args)
    final = release_state(tag)
    if final is None or final["isDraft"]:
        raise ValueError("release remains unpublished after promotion")
    download_and_verify(tag, files)
    print(f"Published and verified {tag}; channel promotion is separate")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("--directory", type=Path, default=Path("dist/release"))
    parser.add_argument("--check", action="store_true", help="validate local inputs without contacting GitHub")
    args = parser.parse_args()
    if args.check:
        validate_set(args.directory, args.tag)
    else:
        publish(args.directory, args.tag)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.SubprocessError) as error:
        raise SystemExit(f"release publication: {error}") from error
