"""Desktop archive integrity and public cask promotion regression tests."""

import base64
import json
from pathlib import Path
import plistlib
import shutil
import tempfile
import unittest
from unittest.mock import patch
from unittest.mock import Mock
import zipfile

import desktop_artifacts as artifacts
from desktop_signing import configuration, notarize
from release_artifacts import digest
from release_desktop import Desktop, CASK, names, reject_downgrade, render
from release_pipeline import applies, load_config, preflight
from release_service import ReleaseError


TAG, SOURCE = "v0.3.1", "a" * 40
REPO, TAP = "Obedience-Corp/obey-voice", "Obedience-Corp/homebrew-tap"
CONFIG = {"repository": TAP, "workflow": "obey-voice-cask.yml", "artifact_workflow": "desktop-release.yml"}


def fixture(directory, notarized=True, extra=None):
    archive = directory / artifacts.archive_name(TAG)
    with zipfile.ZipFile(archive, "w") as zipped:
        prefix = artifacts.APP + "/Contents/"
        info = dict(CFBundleIdentifier=artifacts.BUNDLE_ID, CFBundleShortVersionString=TAG[1:],
                    CFBundleVersion=TAG[1:], LSMinimumSystemVersion="15.0")
        zipped.writestr(prefix + "Info.plist", plistlib.dumps(info))
        zipped.writestr(prefix + "Resources/release.json", json.dumps(
            {"version": TAG, "source_sha": SOURCE, "platform": "macos-arm64"}))
        for name in ("MacOS/obey-voice-agent", "MacOS/obey-voice-engine",
                     "Frameworks/libsherpa-onnx-c-api.dylib", "Frameworks/libonnxruntime.dylib",
                     "Resources/LICENSE", "Resources/NOTICE", "Resources/licenses/rust-dependencies.json",
                     "Resources/licenses/onnxruntime-LICENSE", "Resources/licenses/onnxruntime-ThirdPartyNotices.txt"):
            zipped.writestr(prefix + name, "fixture")
        if extra:
            zipped.writestr(extra, "unexpected")
    checksum = digest(archive)
    receipt = dict(version=TAG, source_sha=SOURCE, platform="macos-arm64", sha256=checksum,
                   notarized=notarized, team_id="TEAM123456", notarization_id="fixture-submission")
    archive.with_suffix(".json").write_text(json.dumps(receipt))
    archive.with_name(archive.name + ".sha256").write_text(f"{checksum}  {archive.name}\n")
    return archive


class FakeGitHub:
    def __init__(self, built, published):
        self.built, self.published = built, published
        self.main = None
        self.branch = None
        self.events = []
        self.fail_verify = False
        self.fail_merge = False

    def file(self, repo, path, ref):
        return (self.main if ref == "main" else self.branch), "blob"

    def ref(self, repo, ref):
        return SOURCE

    def release(self, repo, tag):
        return {"isDraft": False, "assets": [{"name": p.name} for p in self.published.iterdir()]}

    def workflow(self, repo, workflow, ref, sha, inputs=None):
        mode = inputs["mode"] if inputs else "sign"
        self.events.append(mode)
        if mode == "verify" and self.fail_verify:
            raise ReleaseError("cask installation failed")
        if mode == "public":
            assert self.main == self.branch
        return 42

    def download_release(self, repo, tag, directory, names):
        directory.mkdir(parents=True, exist_ok=True)
        for name in names:
            shutil.copyfile(self.published / name, directory / name)

    def gh(self, *args):
        if args[:2] == ("run", "download"):
            directory = Path(args[args.index("--dir") + 1])
            shutil.copytree(self.built, directory, dirs_exist_ok=True)
        elif args[:2] == ("release", "upload"):
            self.events.append("upload")
            path = Path(args[-1])
            shutil.copyfile(path, self.published / path.name)
        elif args[:2] == ("pr", "list"):
            return json.dumps([{"number": 1, "isDraft": False, "url": "https://example.invalid/pr"}])
        else:
            raise AssertionError(args)

    def api(self, path, body=None, method=None):
        assert path.endswith(CASK)
        self.events.append("cask")
        self.branch = base64.b64decode(body["content"]).decode()
        return {"commit": {"sha": SOURCE}}

    def merge(self, repo, pr, sha):
        self.events.append("merge")
        if self.fail_merge:
            raise ReleaseError("review required")
        self.main = self.branch


class DesktopTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.built, self.published = self.root / "built", self.root / "published"
        self.built.mkdir()
        self.published.mkdir()
        self.archive = fixture(self.built)
        self.github = FakeGitHub(self.built, self.published)
        self.adapter = Desktop(self.github, REPO, CONFIG, TAG, SOURCE, lambda *args, **kwargs: None)
        self.public = patch("release_desktop.public_bytes", side_effect=self.readback)
        self.public.start()
        self.addCleanup(self.public.stop)

    def readback(self, url):
        self.github.events.append("readback")
        return (self.published / url.rsplit("/", 1)[1]).read_bytes()

    def test_signing_public_readback_and_install_precede_merge(self):
        self.adapter.run()
        events = self.github.events
        self.assertLess(events.index("sign"), events.index("upload"))
        self.assertLess(events.index("readback"), events.index("cask"))
        self.assertLess(events.index("verify"), events.index("merge"))
        self.assertLess(events.index("merge"), events.index("public"))

    def test_unsigned_preview_never_uploads_or_creates_a_cask(self):
        fixture(self.built, notarized=False)
        with self.assertRaisesRegex(ValueError, "notarization"):
            self.adapter.run()
        self.assertEqual(self.github.events, ["sign"])

    def test_failed_install_does_not_merge(self):
        self.github.fail_verify = True
        with self.assertRaisesRegex(ReleaseError, "installation failed"):
            self.adapter.run()
        self.assertNotIn("merge", self.github.events)

    def test_failed_public_readback_does_not_create_a_cask(self):
        with patch("release_desktop.public_bytes", return_value=b"wrong bytes"):
            with self.assertRaisesRegex(ReleaseError, "readback differs"):
                self.adapter.run()
        self.assertNotIn("cask", self.github.events)

    def test_review_block_resumes_without_resigning_or_replacing_assets(self):
        self.github.fail_merge = True
        with self.assertRaisesRegex(ReleaseError, "review required"):
            self.adapter.run()
        self.github.fail_merge = False
        self.github.events.clear()
        self.adapter.run()
        self.assertNotIn("sign", self.github.events)
        self.assertNotIn("upload", self.github.events)
        self.assertIn("public", self.github.events)

    def test_partial_upload_preserves_existing_bytes(self):
        shutil.copyfile(self.archive, self.published / self.archive.name)
        self.adapter.run()
        self.assertEqual(self.github.events.count("upload"), 2)

    def test_partial_upload_with_different_bytes_is_not_overwritten(self):
        (self.published / self.archive.name).write_bytes(b"different")
        with self.assertRaisesRegex(ReleaseError, "refusing replacement"):
            self.adapter.run()
        self.assertEqual((self.published / self.archive.name).read_bytes(), b"different")

    def test_corruption_and_wrong_source_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "source/platform"):
            artifacts.validate(self.built, TAG, "b" * 40)
        with self.archive.open("ab") as stream:
            stream.write(b"corruption")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            artifacts.validate(self.built, TAG, SOURCE)

    def test_unsafe_archive_paths_are_rejected(self):
        for path in ("../outside", "/outside", artifacts.APP + "/Contents/../outside"):
            fixture(self.built, extra=path)
            with self.assertRaisesRegex(ValueError, "unsafe desktop"):
                artifacts.validate(self.built, TAG, SOURCE)

    def test_downgrade_and_malformed_checksum_are_rejected(self):
        with self.assertRaisesRegex(ReleaseError, "downgrade"):
            reject_downgrade('  version "0.4.0"\n', TAG)
        with self.assertRaisesRegex(ReleaseError, "checksum"):
            render(TAG, '"malicious"')

    def test_original_cli_release_can_resume_before_first_desktop_version(self):
        config = load_config(Path(__file__).resolve().parents[1] / "packaging/release-channels.json")
        channel = config["channels"]["homebrew_cask"]
        self.assertFalse(applies(channel, "v0.3.0"))
        self.assertTrue(applies(channel, "v0.3.1"))

    def test_missing_signing_secrets_block_before_release_mutation(self):
        github = Mock()
        github.api.side_effect = lambda path, **kwargs: (
            {"secrets": []} if "/actions/secrets" in path else
            {"state": "active"} if "/actions/workflows" in path else {"permissions": {"push": True}})
        github.file.side_effect = lambda repo, path, ref: (
            (json.dumps({"schema": 1, "workflow": CONFIG["workflow"], "modes": ["verify", "public"]}), "blob")
            if path.endswith("obey-voice-cask.json") else ("workflow" if path.endswith(".yml") else None, "blob"))
        config = {"repository": REPO, "channels": {"homebrew_cask": dict(CONFIG, enabled=True, since=TAG)}}
        with patch("release_pipeline.native_identity", return_value=(SOURCE, True)):
            with self.assertRaisesRegex(ReleaseError, "signing secrets"):
                preflight(github, config, TAG)
        github.gh.assert_not_called()

    def test_development_identity_cannot_be_used_for_public_signing(self):
        with patch.dict("os.environ", {"APPLE_SIGNING_IDENTITY": "Apple Development: Example (TEAM123456)"}, clear=True):
            with self.assertRaisesRegex(ValueError, "Developer ID Application"):
                configuration()

    def test_rejected_notarization_is_not_stapled_or_promoted(self):
        with patch("desktop_signing.run", side_effect=["", json.dumps({"status": "Invalid", "id": "submission"})]) as run:
            with self.assertRaisesRegex(ValueError, "not accepted"):
                notarize(Path("app"), Path("app.zip"), [])
        self.assertEqual(run.call_count, 2)


if __name__ == "__main__":
    unittest.main()
