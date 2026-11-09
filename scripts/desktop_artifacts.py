#!/usr/bin/env python3
"""Build and validate the versioned desktop archive consumed by the Homebrew cask."""

import argparse
import json
import os
from pathlib import Path, PurePosixPath
import platform
import plistlib
import re
import stat
import subprocess
import tempfile
import zipfile

from desktop_notices import collect
from desktop_signing import SECRET_NAMES, configuration, credentials, notarize, sign, run
from release_artifacts import digest
from release_homebrew import stable_version


ROOT = Path(__file__).resolve().parents[1]
APP = "Obey Voice Agent.app"
BUNDLE_ID = "com.obedience.voice-agent"


def archive_name(tag):
    stable_version(tag)
    return f"obey-voice-app-{tag}-macos-arm64.zip"


def validate(directory, tag, source, allow_unsigned=False):
    archive = directory / archive_name(tag)
    receipt = json.loads(archive.with_suffix(".json").read_text())
    if (receipt.get("version"), receipt.get("source_sha"), receipt.get("platform")) != (tag, source, "macos-arm64"):
        raise ValueError("desktop receipt version/source/platform mismatch")
    if not re.fullmatch(r"[a-f0-9]{40}", source):
        raise ValueError("invalid desktop source commit")
    if not allow_unsigned and (receipt.get("notarized") is not True or
                               not re.fullmatch(r"[A-Z0-9]{10}", receipt.get("team_id", "")) or
                               not receipt.get("notarization_id")):
        raise ValueError("desktop archive has no successful notarization evidence")
    expected = digest(archive)
    if receipt.get("sha256") != expected or archive.with_name(archive.name + ".sha256").read_text() != f"{expected}  {archive.name}\n":
        raise ValueError("desktop archive checksum mismatch")
    with zipfile.ZipFile(archive) as zipped:
        seen = set()
        for entry in zipped.infolist():
            path = PurePosixPath(entry.filename)
            if (path.is_absolute() or ".." in path.parts or not path.parts or
                    entry.filename.rstrip("/") != path.as_posix() or
                    path.parts[0] not in (APP, "__MACOSX") or entry.filename in seen or
                    stat.S_ISLNK(entry.external_attr >> 16)):
                raise ValueError(f"unsafe desktop archive entry: {entry.filename}")
            seen.add(entry.filename)
        info = plistlib.loads(zipped.read(f"{APP}/Contents/Info.plist"))
        if (info.get("CFBundleIdentifier"), info.get("CFBundleShortVersionString"),
                info.get("CFBundleVersion"), info.get("LSMinimumSystemVersion")) != (BUNDLE_ID, tag[1:], tag[1:], "15.0"):
            raise ValueError("desktop bundle identity/version/platform mismatch")
        embedded = json.loads(zipped.read(f"{APP}/Contents/Resources/release.json"))
        if embedded != {"version": tag, "source_sha": source, "platform": "macos-arm64"}:
            raise ValueError("signed desktop provenance does not match receipt")
        for name in ("MacOS/obey-voice-agent", "MacOS/obey-voice-engine",
                     "Frameworks/libsherpa-onnx-c-api.dylib", "Frameworks/libonnxruntime.dylib",
                     "Resources/LICENSE", "Resources/NOTICE", "Resources/licenses/rust-dependencies.json",
                     "Resources/licenses/onnxruntime-LICENSE", "Resources/licenses/onnxruntime-ThirdPartyNotices.txt"):
            if f"{APP}/Contents/{name}" not in seen:
                raise ValueError(f"desktop archive missing {name}")
    return receipt


def smoke(bundle, tag):
    # Exercise the bundled engine from outside the checkout with a disposable home.
    with tempfile.TemporaryDirectory(prefix="obey-desktop-smoke-") as scratch:
        env = {k: v for k, v in os.environ.items()
               if not k.startswith(("DYLD_", "LD_", "OBEY_", "SAMANTHA_"))}
        env.update(HOME=scratch, XDG_CONFIG_HOME=scratch, XDG_CACHE_HOME=scratch)
        executable = bundle / "Contents/MacOS/obey-voice-engine"
        result = subprocess.run([str(executable), "--version"], env=env, cwd=scratch,
                                check=True, capture_output=True, text=True, timeout=30)
        if tag not in result.stdout.split():
            raise ValueError("bundled desktop engine has the wrong version")


def finish(bundle, tag, source, output, identity=None, keychain=None, authentication=None):
    output.mkdir(parents=True, exist_ok=True)
    archive = output / archive_name(tag)
    receipt = {"version": tag, "source_sha": source, "platform": "macos-arm64", "notarized": False}
    for binary in [*(bundle / "Contents/MacOS").iterdir(), *(bundle / "Contents/Frameworks").glob("*.dylib")]:
        if run("lipo", "-archs", str(binary)) != "arm64":
            raise ValueError(f"desktop payload is not native arm64: {binary.name}")
    if identity:
        sign(bundle, identity, keychain, ROOT / "packaging/macos/desktop.entitlements.plist")
        receipt.update(notarized=True, team_id=identity[-11:-1],
                       notarization_id=notarize(bundle, archive, authentication))
    else:
        run("codesign", "--force", "--deep", "--sign", "-", str(bundle))
    smoke(bundle, tag)
    # The final archive is made AFTER stapling. Never upload the submission zip.
    archive.unlink(missing_ok=True)
    run("ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", str(bundle), str(archive))
    receipt["sha256"] = digest(archive)
    archive.with_suffix(".json").write_text(json.dumps(receipt, indent=2) + "\n")
    archive.with_name(archive.name + ".sha256").write_text(f"{receipt['sha256']}  {archive.name}\n")
    validate(output, tag, source, allow_unsigned=identity is None)
    with tempfile.TemporaryDirectory(prefix="obey-desktop-extracted-") as scratch:
        run("ditto", "-x", "-k", str(archive), scratch)
        extracted = Path(scratch) / APP
        run("codesign", "--verify", "--deep", "--strict", str(extracted))
        smoke(extracted, tag)
        if identity:
            run("xcrun", "stapler", "validate", str(extracted))
            run("spctl", "--assess", "--type", "execute", str(extracted))
    print(archive)


def build(tag, output, unsigned=False):
    stable_version(tag)
    if (platform.system(), platform.machine()) != ("Darwin", "arm64"):
        raise ValueError("desktop archives require Apple Silicon macOS")
    source = run("git", "rev-parse", "HEAD")
    if not unsigned and (run("git", "rev-parse", f"{tag}^{{commit}}") != source or
                         run("git", "status", "--porcelain")):
        raise ValueError("public desktop archive requires a clean checkout of its release tag")

    def prepare():
        env = dict(os.environ, OBEY_VOICE_DESKTOP_VERSION=tag, MACOSX_DEPLOYMENT_TARGET="15.0",
                   CGO_ENABLED="1", GOOS="darwin", GOARCH="arm64")
        subprocess.run(["just", "desktop", "bundle-release"], env=env, check=True)
        bundle = ROOT / "apps/desktop/dist" / APP
        collect(ROOT, bundle)
        (bundle / "Contents/Resources/release.json").write_text(json.dumps(
            {"version": tag, "source_sha": source, "platform": "macos-arm64"}, indent=2) + "\n")
        return bundle

    if not unsigned:
        configuration()  # Fail before compiling if public signing is not configured.
    # Dependency build scripts do not receive signing credentials. Import the
    # private key only after the unprivileged build and notice collection finish.
    withheld = {name: os.environ.pop(name) for name in (*SECRET_NAMES, "APPLE_NOTARY_PROFILE") if name in os.environ}
    try:
        bundle = prepare()
    finally:
        os.environ.update(withheld)
    if unsigned:
        finish(bundle, tag, source, output)
    else:
        with credentials() as (identity, keychain, authentication):
            finish(bundle, tag, source, output, identity, keychain, authentication)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("--unsigned", action="store_true", help="local/CI preview only; rejected by release promotion")
    parser.add_argument("--output", type=Path, default=ROOT / "dist/desktop-release")
    args = parser.parse_args()
    os.chdir(ROOT)
    build(args.tag, args.output, args.unsigned)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        raise SystemExit(f"desktop artifact: {error}") from error
