"""Native distribution integrity checks, using disposable payloads without audio."""

import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import release_artifacts as artifacts


class PayloadFixture(unittest.TestCase):
    def setUp(self):
        self.stdout = patch("sys.stdout", new_callable=io.StringIO)
        self.stdout.start()
        self.addCleanup(self.stdout.stop)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.output = self.root / "release"
        self.tag = "v1.2.3"

    def payload(self, platform="linux-amd64"):
        stage = self.root / f"obey-voice-{platform}"
        for name in artifacts.required_files(platform):
            path = stage / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture: " + name)
        (stage / "obey-voice").chmod(0o755)
        artifacts.package(stage, platform, self.tag, self.output)
        return self.output / artifacts.archive_name(self.tag, platform)

    def rewrite_archive(self, archive, modify):
        with tarfile.open(archive, "r:gz") as tar:
            entries = [(entry, tar.extractfile(entry).read() if entry.isfile() else None)
                       for entry in tar.getmembers()]
        with tarfile.open(archive, "w:gz") as tar:
            for entry, data in entries:
                changed = modify(entry, data)
                if changed is None:
                    continue
                entry, data = changed
                if data is not None:
                    entry.size = len(data)
                tar.addfile(entry, io.BytesIO(data) if data is not None else None)
        archive.with_name(archive.name + ".sha256").write_text(
            f"{artifacts.digest(archive)}  {archive.name}\n")


class ArtifactTests(PayloadFixture):
    def test_both_platforms_and_local_alias(self):
        for platform in artifacts.PLATFORMS:
            archive = self.payload(platform)
            artifacts.verify(archive, self.tag, platform)
            self.assertEqual(artifacts.digest(archive), artifacts.digest(
                self.root / f"obey-voice-{platform}.tar.gz"))

    def test_archive_corruption(self):
        archive = self.payload()
        with archive.open("ab") as file:
            file.write(b"corrupted")
        with self.assertRaisesRegex(ValueError, "archive checksum mismatch"):
            artifacts.verify(archive, self.tag, "linux-amd64")

    def test_payload_corruption_even_with_updated_archive_checksum(self):
        archive = self.payload()
        self.rewrite_archive(archive, lambda entry, data:
                             (entry, b"changed") if entry.name.endswith("/LICENSE") else (entry, data))
        with self.assertRaisesRegex(ValueError, "payload checksum mismatch"):
            artifacts.verify(archive, self.tag, "linux-amd64")

    def test_missing_library_even_with_updated_manifest(self):
        archive = self.payload()
        def remove_library(entry, data):
            if entry.name.endswith("libonnxruntime.so"):
                return None
            if entry.name.endswith("manifest.json"):
                manifest = json.loads(data)
                manifest["files"] = [row for row in manifest["files"]
                                     if row["path"] != "lib/libonnxruntime.so"]
                data = json.dumps(manifest).encode()
            return entry, data
        self.rewrite_archive(archive, remove_library)
        with self.assertRaisesRegex(ValueError, "missing required native"):
            artifacts.verify(archive, self.tag, "linux-amd64")

    def test_wrong_manifest_version(self):
        archive = self.payload()
        def wrong_version(entry, data):
            if entry.name.endswith("manifest.json"):
                data = data.replace(b"v1.2.3", b"v1.2.4")
            return entry, data
        self.rewrite_archive(archive, wrong_version)
        with self.assertRaisesRegex(ValueError, "manifest identity"):
            artifacts.verify(archive, self.tag, "linux-amd64")

    def test_archive_traversal(self):
        archive = self.payload()
        def traversal(entry, data):
            if entry.name.endswith("/NOTICE"):
                entry.name = "../outside"
            return entry, data
        self.rewrite_archive(archive, traversal)
        with self.assertRaisesRegex(ValueError, "unsafe archive entry"):
            artifacts.verify(archive, self.tag, "linux-amd64")

    def test_symlink_not_packaged(self):
        self.payload()
        stage = self.root / "obey-voice-linux-amd64"
        (stage / "link").symlink_to(stage / "LICENSE")
        with self.assertRaisesRegex(ValueError, "not symlinks"):
            artifacts.package(stage, "linux-amd64", self.tag, self.output)

    def test_tag_version_is_exact_not_nearest_git_describe(self):
        with patch.dict(os.environ, {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": self.tag}), \
                patch.object(artifacts.subprocess, "check_output", side_effect=["abc\n", "abc\n"]):
            self.assertEqual(artifacts.version(), self.tag)

    def test_wrong_tag_checkout(self):
        with patch.dict(os.environ, {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": self.tag}), \
                patch.object(artifacts.subprocess, "check_output", side_effect=["abc\n", "def\n"]):
            with self.assertRaisesRegex(ValueError, "checked-out commit"):
                artifacts.version()

    def test_unsafe_version_and_unsupported_platform(self):
        for version, platform in (("../escape", "linux-amd64"), (self.tag, "linux-arm64")):
            with self.assertRaises(ValueError):
                artifacts.archive_name(version, platform)


if __name__ == "__main__":
    unittest.main()
