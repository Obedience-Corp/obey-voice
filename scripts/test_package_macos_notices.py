"""Fail-closed license packaging checks; no network, models or audio devices."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().with_name("package-macos-notices.sh")


class PackageMacOSNoticesTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.stage = self.root / "stage"
        (self.stage / "lib").mkdir(parents=True)
        self.runtime = self.stage / "lib/libonnxruntime.dylib"
        self.runtime.write_bytes(b"1.28.2\n")
        self.module = self.root / "module"
        self.module.mkdir()
        (self.module / "LICENSE").write_text("module fixture license")
        (self.root / "LICENSE").write_text("project fixture license")
        (self.root / "NOTICE").write_text("project fixture notice")
        fakebin = self.root / "bin"
        fakebin.mkdir()
        # Downloaded content must match the real pinned digests, even after HTTP
        # succeeds. This stub prevents all network access and supplies bad bytes.
        curl = fakebin / "curl"
        curl.write_text('#!/bin/sh\nprintf "unexpected upstream content" > "$4"\n')
        curl.chmod(0o755)
        self.env = dict(os.environ, PATH=str(fakebin) + os.pathsep + os.environ["PATH"])

    def run_packaging(self):
        return subprocess.run(["bash", str(SCRIPT), str(self.stage), str(self.module)],
                              cwd=self.root, env=self.env, capture_output=True, text=True)

    def test_unreviewed_native_dependency_fails(self):
        (self.stage / "lib/new-runtime.dylib").write_bytes(b"library")
        result = self.run_packaging()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unreviewed native dependency", result.stderr)

    def test_runtime_version_drift_fails(self):
        self.runtime.write_bytes(b"1.99.0\n")
        result = self.run_packaging()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no longer matches license version", result.stderr)

    def test_missing_module_license_fails(self):
        (self.module / "LICENSE").unlink()
        result = self.run_packaging()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("LICENSE", result.stderr)

    def test_downloaded_notice_digest_drift_fails(self):
        result = self.run_packaging()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("FAILED", result.stdout)


if __name__ == "__main__":
    unittest.main()
