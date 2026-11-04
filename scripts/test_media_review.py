"""Negative paths for the public asset gate; no models, audio or user config."""

import hashlib
from pathlib import Path
import tempfile
import unittest

from media_review import check


class MediaReviewTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "sample.wav").write_bytes(b"fixture")
        self.entry = {
            "path": "sample.wav",
            "sha256": hashlib.sha256(b"fixture").hexdigest(),
            "source": "test fixture",
            "disposition": "retained_private_pending_review",
            "content_review": "pending",
            "public_release": "blocked",
        }

    def inspect(self, entries=None, tracked=None):
        return check(self.root, {"assets": entries if entries is not None else [self.entry]},
                     tracked if tracked is not None else {"sample.wav"})

    def test_inventory_integrity_does_not_imply_public_clearance(self):
        self.assertEqual(self.inspect(), ([], ["sample.wav"]))

    def test_new_media_requires_record(self):
        errors, _ = self.inspect(tracked={"sample.wav", "new.svg", "font.woff2"})
        self.assertEqual(errors, ["unlisted tracked media: font.woff2",
                                  "unlisted tracked media: new.svg"])

    def test_changed_bytes_invalidate_record(self):
        (self.root / "sample.wav").write_bytes(b"different speech")
        self.assertIn("asset changed", self.inspect()[0][0])

    def test_cleared_status_requires_evidence(self):
        self.entry["public_release"] = "cleared"
        errors, _ = self.inspect()
        self.assertTrue(any("rights_evidence" in error for error in errors))
        self.assertTrue(any("content_evidence" in error for error in errors))
        self.assertTrue(any("retain_public" in error for error in errors))

    def test_complete_record_is_valid_but_tool_does_not_adjudicate_rights(self):
        self.entry.update(public_release="cleared", disposition="retain_public",
                          content_review="complete", review={
                              "reviewer": "fixture reviewer", "reviewed_at": "2026-11-03",
                              "content_evidence": "fixture review reference",
                              "rights_evidence": "fixture permission reference",
                          })
        self.assertEqual(self.inspect(), ([], []))

    def test_removed_asset_cannot_keep_stale_record(self):
        self.assertIn("not tracked", self.inspect(tracked=set())[0][0])

    def test_duplicate_record_fails(self):
        self.assertIn("duplicate asset: sample.wav",
                      self.inspect(entries=[self.entry, self.entry])[0])

    def test_traversal_and_symlink_fail(self):
        self.entry["path"] = "../outside.wav"
        self.assertIn("invalid asset path", self.inspect()[0][0])
        self.entry["path"] = "sample.wav"
        (self.root / "sample.wav").unlink()
        (self.root / "sample.wav").symlink_to(self.root / "missing")
        self.assertIn("regular file", self.inspect()[0][0])

    def test_malformed_input_fails_without_crashing(self):
        self.assertTrue(check(self.root, [], set())[0])
        self.assertTrue(self.inspect(entries=[None, {"path": 42}])[0])
        self.entry.update(public_release="cleared", review=None)
        self.assertTrue(self.inspect()[0])


if __name__ == "__main__":
    unittest.main()
