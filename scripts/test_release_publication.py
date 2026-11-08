"""Exercise draft publication, retries, and readback with an in-memory gh service."""

import json
from pathlib import Path
import subprocess
from unittest.mock import patch

import publish_release as publisher
from test_release_artifacts import PayloadFixture


class PublicationTests(PayloadFixture):
    def setUp(self):
        super().setUp()
        for platform in publisher.PLATFORMS:
            self.payload(platform)
        self.remote = {}
        self.draft = None
        self.calls = []
        self.corrupt_download = False

    def gh(self, *args):
        self.calls.append(args)
        command = args[1]
        if command == "view":
            if self.draft is None:
                raise subprocess.CalledProcessError(1, "gh", stderr="release not found")
            return json.dumps({"tagName": self.tag, "isDraft": self.draft, "assets": []})
        if command == "create":
            self.assertIn("--draft", args)
            self.assertIn("--verify-tag", args)
            self.draft = True
        elif command == "upload":
            self.assertTrue(self.draft, "published release must never be mutated")
            for name in args[3:-1]:
                path = Path(name)
                self.remote[path.name] = path.read_bytes()
        elif command == "download":
            directory = Path(args[args.index("--dir") + 1])
            for name, contents in self.remote.items():
                (directory / name).write_bytes(contents + (b"bad" if self.corrupt_download else b""))
        elif command == "edit":
            self.assertIn("--latest=false", args)
            self.draft = False
        else:
            self.fail(f"unexpected gh command: {args}")
        return ""

    def publish(self):
        with patch.object(publisher, "gh", side_effect=self.gh):
            publisher.publish(self.output, self.tag)

    def test_complete_draft_before_publication_and_readback_after(self):
        self.publish()
        self.assertFalse(self.draft)
        self.assertEqual([call[1] for call in self.calls],
                         ["view", "create", "upload", "download", "edit", "view", "download"])

    def test_incomplete_release_does_not_contact_github(self):
        next(self.output.glob("*macos-arm64.tar.gz")).unlink()
        with self.assertRaises(FileNotFoundError):
            self.publish()
        self.assertEqual(self.calls, [])

    def test_failed_readback_leaves_draft_and_retry_completes(self):
        self.corrupt_download = True
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.publish()
        self.assertTrue(self.draft)
        self.assertFalse(any(call[1] == "edit" for call in self.calls))
        self.corrupt_download = False
        self.publish()
        self.assertFalse(self.draft)
        self.assertEqual(sum(call[1] == "create" for call in self.calls), 1)

    def test_published_retry_is_read_only(self):
        self.publish()
        self.calls.clear()
        self.publish()
        self.assertEqual([call[1] for call in self.calls], ["view", "download"])

    def test_published_mismatch_is_not_replaced(self):
        self.publish()
        self.calls.clear()
        self.corrupt_download = True
        with self.assertRaises(ValueError):
            self.publish()
        self.assertEqual([call[1] for call in self.calls], ["view", "download"])

    def test_authentication_failure_is_not_treated_as_missing_release(self):
        error = subprocess.CalledProcessError(1, "gh", stderr="HTTP 401: authentication required")
        with patch.object(publisher, "gh", side_effect=error) as mock:
            with self.assertRaises(subprocess.CalledProcessError):
                publisher.publish(self.output, self.tag)
            self.assertEqual(mock.call_count, 1)
