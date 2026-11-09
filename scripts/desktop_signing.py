"""Isolated CI signing credentials and Developer ID notarization."""

import base64
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile


SECRET_NAMES = ("APPLE_CERTIFICATE_P12", "APPLE_CERTIFICATE_PASSWORD", "APPLE_SIGNING_IDENTITY",
                "APPLE_NOTARY_KEY_ID", "APPLE_NOTARY_ISSUER_ID", "APPLE_NOTARY_PRIVATE_KEY")


def run(*args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=1800)
    if result.returncode:
        # Commands may contain credentials. Never include argv in an exception.
        raise ValueError(f"{Path(args[0]).name} failed (exit {result.returncode})")
    return result.stdout.strip()


def configuration():
    identity = os.environ.get("APPLE_SIGNING_IDENTITY", "")
    if not re.fullmatch(r"Developer ID Application: .+ \([A-Z0-9]{10}\)", identity):
        raise ValueError("APPLE_SIGNING_IDENTITY must name a Developer ID Application certificate")
    profile = os.environ.get("APPLE_NOTARY_PROFILE")
    missing = [] if profile else [name for name in SECRET_NAMES if not os.environ.get(name)]
    if missing:
        raise ValueError("missing desktop signing credentials: " + ", ".join(missing))
    return identity, profile


@contextmanager
def credentials():
    identity, profile = configuration()
    if profile:
        yield identity, [], ["--keychain-profile", profile]
        return
    with tempfile.TemporaryDirectory(prefix="obey-desktop-signing-") as scratch:
        scratch = Path(scratch)
        keychain = str(scratch / "signing.keychain-db")
        certificate = scratch / "certificate.p12"
        private_key = scratch / "notary.p8"
        certificate.write_bytes(base64.b64decode(os.environ["APPLE_CERTIFICATE_P12"], validate=True))
        private_key.write_text(os.environ["APPLE_NOTARY_PRIVATE_KEY"])
        certificate.chmod(0o600)
        private_key.chmod(0o600)
        password = secrets.token_hex(32)
        run("security", "create-keychain", "-p", password, keychain)
        try:
            run("security", "set-keychain-settings", "-lut", "21600", keychain)
            run("security", "unlock-keychain", "-p", password, keychain)
            run("security", "import", str(certificate), "-k", keychain, "-P",
                os.environ["APPLE_CERTIFICATE_PASSWORD"], "-T", "/usr/bin/codesign")
            run("security", "set-key-partition-list", "-S", "apple-tool:,apple:,codesign:",
                "-s", "-k", password, keychain)
            run("xcrun", "notarytool", "store-credentials", "obey-release", "--key", str(private_key),
                "--key-id", os.environ["APPLE_NOTARY_KEY_ID"],
                "--issuer", os.environ["APPLE_NOTARY_ISSUER_ID"], "--keychain", keychain)
            yield identity, ["--keychain", keychain], ["--keychain-profile", "obey-release", "--keychain", keychain]
        finally:
            run("security", "delete-keychain", keychain)


def sign(bundle, identity, keychain, entitlements):
    for library in sorted((bundle / "Contents/Frameworks").glob("*.dylib")):
        run("codesign", "--force", "--timestamp", "--options", "runtime", "--sign", identity,
            *keychain, str(library))
    # Sign inside-out. --deep is verification only, never used to repair signing.
    for executable in sorted((bundle / "Contents/MacOS").iterdir()):
        run("codesign", "--force", "--timestamp", "--options", "runtime", "--entitlements",
            str(entitlements), "--sign", identity, *keychain, str(executable))
    run("codesign", "--force", "--timestamp", "--options", "runtime", "--entitlements",
        str(entitlements), "--sign", identity, *keychain, str(bundle))
    run("codesign", "--verify", "--deep", "--strict", str(bundle))


def notarize(bundle, archive, authentication):
    run("ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", str(bundle), str(archive))
    result = json.loads(run("xcrun", "notarytool", "submit", str(archive), *authentication,
                            "--wait", "--timeout", "25m", "--output-format", "json"))
    if result.get("status") != "Accepted":
        raise ValueError(f"Apple notarization was not accepted (submission {result.get('id')})")
    run("xcrun", "stapler", "staple", str(bundle))
    run("xcrun", "stapler", "validate", str(bundle))
    run("spctl", "--assess", "--type", "execute", "--verbose=2", str(bundle))
    return result["id"]
