"""Install lifecycle tests with executable fixtures and disposable prefixes."""

import io
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import install_dev
import release_artifacts as artifacts


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.destination = self.root / "Go bin with spaces ' $"
        self.destination.mkdir()
        self.entry = self.destination / "obey-voice"
        self.target = "linux-amd64"
        self.value = "v0.3.0-7-g1234567-dirty"
        self.stdout = patch("sys.stdout", new_callable=io.StringIO)
        self.stdout.start()
        self.addCleanup(self.stdout.stop)

    def payload(self, script=None):
        stage = self.root / "stage"
        for name in artifacts.required_files(self.target):
            path = stage / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture " + name)
        executable = stage / "obey-voice"
        executable.write_text(script or f"#!/bin/sh\necho obey-voice {self.value}\n")
        executable.chmod(0o755)
        output = self.root / "release"
        artifacts.package(stage, self.target, self.value, output)
        return output / artifacts.archive_name(self.value, self.target)

    def install(self, archive):
        return install_dev.install(archive, self.value, self.target, self.destination)

    def test_replaces_raw_executable_and_runs_through_both_entry_points(self):
        self.entry.write_text("old executable")
        self.install(self.payload())
        self.assertTrue(self.entry.is_symlink())
        for name in ("obey-voice", "samantha"):
            result = subprocess.check_output([str(self.destination / name), "--version"], text=True)
            self.assertIn(self.value, result)
        self.assertTrue((self.entry.resolve().parent / "lib/libonnxruntime.so").is_file())

    def test_release_symlink_target_is_preserved_and_reinstall_retires_only_dev_payload(self):
        release = self.root / "released-executable"
        release.write_text("release must stay unchanged")
        self.entry.symlink_to(release)
        archive = self.payload()
        self.install(archive)
        first_payload = self.entry.resolve().parent.parent
        self.install(archive)
        self.assertFalse(first_payload.exists())
        self.assertTrue(self.entry.exists())
        self.assertEqual(release.read_text(), "release must stay unchanged")
        self.assertEqual(len(list((self.destination / ".obey-voice-dev").iterdir())), 1)

    def test_corruption_preserves_working_install(self):
        archive = self.payload()
        self.install(archive)
        previous = self.entry.resolve()
        with archive.open("ab") as stream:
            stream.write(b"corrupt")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.install(archive)
        self.assertEqual(self.entry.resolve(), previous)
        self.assertTrue(previous.exists())

    def test_failed_startup_preserves_old_binary_and_cleans_staging(self):
        self.entry.write_text("working install")
        archive = self.payload("#!/bin/sh\nexit 7\n")
        with self.assertRaises(subprocess.CalledProcessError):
            self.install(archive)
        self.assertEqual(self.entry.read_text(), "working install")
        self.assertEqual(list((self.destination / ".obey-voice-dev").iterdir()), [])

    def test_wrong_version_preserves_old_binary(self):
        self.entry.write_text("working install")
        with self.assertRaisesRegex(ValueError, "installed version does not match"):
            self.install(self.payload("#!/bin/sh\necho obey-voice v0.0.0\n"))
        self.assertEqual(self.entry.read_text(), "working install")

    def test_directory_collision_preserves_old_binary(self):
        self.entry.write_text("working install")
        (self.destination / "samantha").mkdir()
        with self.assertRaisesRegex(ValueError, "install entry is a directory"):
            self.install(self.payload())
        self.assertEqual(self.entry.read_text(), "working install")


if __name__ == "__main__":
    unittest.main()
