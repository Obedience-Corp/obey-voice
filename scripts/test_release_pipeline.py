"""Publication ordering, partial recovery and channel rejection regression tests."""

import base64
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

from release_homebrew import Homebrew, TAGS, bottle_formula, reject_downgrade, validate_bottles
from release_pipeline import check_npm_downgrade, execute, load_config, verify_npm_package
from release_service import GitHub, ReleaseError


REPO = "Obedience-Corp/homebrew-tap"
TAG = "v0.3.0"
SOURCE = "a" * 40
BASE = '''class ObeyVoice < Formula
  url "https://github.com/Obedience-Corp/obey-voice/releases/download/v0.3.0/obey-voice-v0.3.0-linux-amd64.tar.gz"
  license "AGPL-3.0-only"
end
'''


def fixture(directory):
    for target in TAGS:
        archive = directory / f"obey-voice-0.3.0.{target}.bottle.tar.gz"
        archive.write_bytes(f"synthetic bottle: {target}".encode())
        metadata = {"obedience-corp/tap/obey-voice": {
            "formula": {"pkg_version": "0.3.0", "tap_git_revision": SOURCE,
                        "tap_git_path": "Formula/obey-voice.rb", "tap_git_remote": f"https://github.com/{REPO}"},
            "bottle": {"root_url": f"https://github.com/{REPO}/releases/download/obey-voice-v0.3.0",
                       "rebuild": 0, "cellar": "any", "tags": {target: {
                           "filename": archive.name,
                           "local_filename": archive.name.replace("voice-0", "voice--0"),
                           "sha256": hashlib.sha256(archive.read_bytes()).hexdigest()}}}}}
        (directory / f"obey-voice--0.3.0.{target}.bottle.json").write_text(json.dumps(metadata))


class FakeGitHub:
    def __init__(self, directory):
        self.directory = directory
        self.main = BASE.replace("v0.3.0", "v0.2.0")
        self.branch = None
        self.events = []
        self.draft = True
        self.fail_mode = None
        self.block_merge = False
        self.downgrade_before_publish = False

    def file(self, repo, path, ref):
        return (BASE if ref == SOURCE else self.main if ref == "main" else self.branch), "blob"

    def ref(self, repo, ref):
        return "b" * 40

    def release(self, repo, tag):
        return {"isDraft": self.draft, "url": "https://example.invalid/release",
                "assets": [{"name": p.name} for p in self.directory.iterdir()]}

    def download_release(self, repo, tag, directory, names):
        directory.mkdir(parents=True, exist_ok=True)
        for name in names:
            shutil.copyfile(self.directory / name, directory / name)

    def update_formula(self, repo, branch, text):
        self.branch = text
        self.events.append("formula")
        return "b" * 40

    def pr(self, repo, branch, tag):
        return {"number": 1, "isDraft": True, "url": "https://example.invalid/pr"}

    def workflow(self, repo, workflow, ref, sha, inputs):
        mode = inputs["mode"]
        self.events.append(mode)
        if mode == self.fail_mode:
            raise ReleaseError("synthetic workflow failure")
        if mode == "verify" and self.downgrade_before_publish:
            self.main = BASE.replace("v0.3.0", "v0.4.0")
        if mode == "public":
            assert not self.draft
            assert "bottle do" in self.main
        return 42

    def gh(self, *args):
        if args[:2] != ("release", "edit"):
            raise AssertionError(args)
        self.events.append("publish")
        self.draft = False
        return ""

    def merge(self, repo, pr, sha):
        self.events.append("merge")
        if self.block_merge:
            raise ReleaseError("review required")
        assert not self.draft
        self.main = self.branch


class ReleasePipelineTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        fixture(self.directory)
        self.github = FakeGitHub(self.directory)
        self.reports = []
        self.adapter = Homebrew(self.github, {"repository": REPO, "workflow": "bottles.yml"},
                                TAG, BASE, lambda *args, **kwargs: self.reports.append((args, kwargs)))
        self.readback = patch("release_homebrew.public_bytes",
                              side_effect=lambda url: (self.directory / url.rsplit("/", 1)[1]).read_bytes())
        self.readback.start()
        self.addCleanup(self.readback.stop)

    def edit_metadata(self, target, mutate):
        path = self.directory / f"obey-voice--0.3.0.{target}.bottle.json"
        document = json.loads(path.read_text())
        mutate(document["obedience-corp/tap/obey-voice"])
        path.write_text(json.dumps(document))

    def test_publish_readback_precedes_merge_and_public_install(self):
        self.adapter.run()
        self.assertEqual(self.github.events, ["formula", "verify", "publish", "merge", "public"])
        self.assertEqual(self.reports[-1][0], ("homebrew", "verified"))

    def test_staged_install_failure_never_publishes_or_merges(self):
        self.github.fail_mode = "verify"
        with self.assertRaisesRegex(ReleaseError, "workflow failure"):
            self.adapter.run()
        self.assertTrue(self.github.draft)
        self.assertNotIn("merge", self.github.events)

    def test_public_readback_mismatch_never_merges(self):
        with patch("release_homebrew.public_bytes", return_value=b"wrong bytes"):
            with self.assertRaisesRegex(ReleaseError, "readback differs"):
                self.adapter.run()
        self.assertNotIn("merge", self.github.events)

    def test_retry_after_review_block_reuses_published_bytes(self):
        self.github.block_merge = True
        with self.assertRaisesRegex(ReleaseError, "review required"):
            self.adapter.run()
        self.assertFalse(self.github.draft)
        self.github.block_merge = False
        self.github.events.clear()
        self.adapter.run()
        self.assertNotIn("publish", self.github.events)
        self.assertNotIn("build", self.github.events)
        self.assertEqual(self.github.main, self.github.branch)

    def test_already_merged_draft_is_recovered_before_claiming_success(self):
        entries, _ = validate_bottles(self.directory, TAG, REPO)
        self.github.main = bottle_formula(BASE, entries, TAG, REPO)
        self.adapter.run()
        self.assertIn("publish", self.github.events)
        self.assertNotIn("merge", self.github.events)
        self.assertEqual(self.github.events[-1], "public")

    def test_newer_release_during_build_prevents_old_publication(self):
        self.github.downgrade_before_publish = True
        with self.assertRaisesRegex(ReleaseError, "downgrade"):
            self.adapter.run()
        self.assertTrue(self.github.draft)
        self.assertNotIn("merge", self.github.events)

    def test_wrong_bottle_source_formula_is_rejected(self):
        original = self.github.file
        self.github.file = lambda repo, path, ref: ((BASE + "changed install\n", "blob")
                                                   if ref == SOURCE else original(repo, path, ref))
        with self.assertRaisesRegex(ReleaseError, "verified native assets"):
            self.adapter.run()
        self.assertEqual(self.github.events, [])

    def test_corrupt_bottle_is_rejected(self):
        (self.directory / "obey-voice-0.3.0.arm64_sequoia.bottle.tar.gz").write_bytes(b"bad")
        with self.assertRaisesRegex(ReleaseError, "checksum mismatch"):
            validate_bottles(self.directory, TAG, REPO)

    def test_cross_commit_platform_pair_is_rejected(self):
        self.edit_metadata(TAGS[0], lambda data: data["formula"].update(tap_git_revision="c" * 40))
        with self.assertRaisesRegex(ReleaseError, "different tap commits"):
            validate_bottles(self.directory, TAG, REPO)

    def test_metadata_cannot_escape_download_directory(self):
        self.edit_metadata(TAGS[0], lambda data: data["bottle"]["tags"][TAGS[0]].update(filename="../outside"))
        with self.assertRaisesRegex(ReleaseError, "filename"):
            validate_bottles(self.directory, TAG, REPO)

    def test_missing_platform_is_not_accepted(self):
        (self.directory / "obey-voice--0.3.0.x86_64_linux.bottle.json").unlink()
        with self.assertRaises(FileNotFoundError):
            validate_bottles(self.directory, TAG, REPO)

    def test_published_partial_release_cannot_be_replaced(self):
        (self.directory / "obey-voice--0.3.0.x86_64_linux.bottle.json").unlink()
        self.github.draft = False
        with self.assertRaisesRegex(ReleaseError, "incomplete"):
            self.adapter.run()
        self.assertEqual(self.github.events, [])

    def test_partial_draft_upload_resumes_without_clobbering_assets(self):
        with tempfile.TemporaryDirectory() as build_dir, tempfile.TemporaryDirectory() as stage_dir:
            build = Path(build_dir)
            fixture(build)
            entries, _ = validate_bottles(build, TAG, REPO)
            existing = self.directory / "obey-voice-0.3.0.arm64_sequoia.bottle.tar.gz"
            for path in self.directory.iterdir():
                if path != existing:
                    path.unlink()
            original = existing.read_bytes()
            def upload(*args):
                self.assertEqual(args[:2], ("release", "upload"))
                self.assertNotIn("--clobber", args)
                path = Path(args[-1])
                shutil.copyfile(path, self.directory / path.name)
                return ""
            with patch.object(self.github, "gh", side_effect=upload) as gh:
                self.adapter.stage(Path(stage_dir), entries, SOURCE)
            self.assertEqual(gh.call_count, 3)
            self.assertEqual(existing.read_bytes(), original)
            validate_bottles(self.directory, TAG, REPO)

    def test_conflicting_partial_draft_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as build_dir, tempfile.TemporaryDirectory() as stage_dir:
            fixture(Path(build_dir))
            entries, _ = validate_bottles(Path(build_dir), TAG, REPO)
            existing = self.directory / "obey-voice-0.3.0.arm64_sequoia.bottle.tar.gz"
            existing.write_bytes(b"different previously staged artifact")
            with self.assertRaisesRegex(ReleaseError, "different bytes"):
                self.adapter.stage(Path(stage_dir), entries, SOURCE)
            self.assertEqual(existing.read_bytes(), b"different previously staged artifact")

    def test_semantic_downgrade_handles_two_digit_components(self):
        with self.assertRaisesRegex(ReleaseError, "downgrade"):
            reject_downgrade(BASE.replace("v0.3.0", "v0.10.0"), "v0.9.0")

    def test_disabled_npm_never_claims_publication(self):
        config = {"repository": "Obedience-Corp/obey-voice",
                  "channels": {"npm": {"enabled": False, "reason": "not packaged"}}}
        reports = []
        with patch("release_pipeline.preflight", return_value={"source_sha": SOURCE, "resume": True}), \
                patch("release_pipeline.native_release"):
            execute(self.github, config, TAG, lambda *args, **kwargs: reports.append((args, kwargs)))
        self.assertEqual(reports, [(("npm", "not-configured"), {"reason": "not packaged"})])
        self.assertEqual(self.github.events, [])

    def test_enabling_unknown_adapter_or_empty_npm_fails_closed(self):
        path = self.directory / "config.json"
        for channels in ({"future": {"enabled": True, "workflow": "release.yml"}},
                         {"npm": {"enabled": True, "workflow": "release.yml", "packages": []}}):
            path.write_text(json.dumps({"repository": "Obedience-Corp/obey-voice", "channels": channels}))
            with self.assertRaises(ReleaseError):
                load_config(path)

    def test_npm_verifies_source_and_public_tarball_integrity(self):
        content = b"synthetic npm tarball"
        metadata = {"name": "@obey/voice", "version": "0.3.0", "gitHead": SOURCE,
                    "dist": {"tarball": "https://registry.npmjs.org/voice.tgz",
                             "integrity": "sha512-" + base64.b64encode(hashlib.sha512(content).digest()).decode()}}
        with patch("release_pipeline.public_bytes", side_effect=[json.dumps(metadata).encode(), content]):
            verify_npm_package("@obey/voice", TAG, SOURCE)
        with patch("release_pipeline.public_bytes", side_effect=[json.dumps(metadata).encode(), b"corrupt"]):
            with self.assertRaisesRegex(ReleaseError, "integrity mismatch"):
                verify_npm_package("@obey/voice", TAG, SOURCE)
        metadata["gitHead"] = "d" * 40
        with patch("release_pipeline.public_bytes", return_value=json.dumps(metadata).encode()):
            with self.assertRaisesRegex(ReleaseError, "source does not match"):
                verify_npm_package("@obey/voice", TAG, SOURCE)

    def test_npm_latest_downgrade_is_rejected_before_dispatch(self):
        with patch("release_pipeline.public_bytes", return_value=b'{"version":"0.10.0"}'):
            with self.assertRaisesRegex(ReleaseError, "downgrade"):
                check_npm_downgrade(["@obey/voice"], "v0.9.0")


class WorkflowSelectionTests(unittest.TestCase):
    def test_formula_update_uses_contents_put_with_existing_blob_guard(self):
        github = GitHub()
        with patch.object(github, "file", return_value=("old formula", "old-blob")), \
                patch("release_service.command", return_value=json.dumps({"commit": {"sha": SOURCE}})) as call:
            self.assertEqual(github.update_formula(REPO, "release", "new formula"), SOURCE)
            args = call.call_args.args
            self.assertIn("--method", args)
            self.assertEqual(args[args.index("--method") + 1], "PUT")
            request = json.loads(call.call_args.kwargs["data"])
            self.assertEqual(request["sha"], "old-blob")
            self.assertEqual(request["branch"], "release")

    def test_verify_does_not_reuse_successful_build_run(self):
        github = GitHub(timeout=10, poll=0)
        runs = [[{"databaseId": 1, "headSha": SOURCE, "event": "workflow_dispatch",
                  "displayTitle": "Obey Voice build @ release"}],
                [{"databaseId": 2, "headSha": SOURCE, "event": "workflow_dispatch",
                  "displayTitle": "Obey Voice verify @ release"}]]
        def gh(*args):
            if args[:2] == ("run", "list"):
                return json.dumps(runs.pop(0))
            self.assertEqual(args[:2], ("workflow", "run"))
            return ""
        with patch.object(github, "gh", side_effect=gh), patch.object(github, "api", return_value={
                "head_sha": SOURCE, "status": "completed", "conclusion": "success", "run_attempt": 1}):
            self.assertEqual(github.workflow(REPO, "bottles.yml", "release", SOURCE, {"mode": "verify"}), 2)

    def test_failure_rerun_waits_for_new_attempt(self):
        github = GitHub(timeout=10, poll=0)
        old = {"head_sha": SOURCE, "status": "completed", "conclusion": "failure", "run_attempt": 1}
        new = {**old, "conclusion": "success", "run_attempt": 2}
        with patch.object(github, "gh", side_effect=[json.dumps([{
                "databaseId": 1, "headSha": SOURCE, "event": "workflow_dispatch", "displayTitle": "release"}]), ""]), \
                patch.object(github, "api", side_effect=[old, old, new]):
            self.assertEqual(github.workflow(REPO, "release.yml", "v0.3.0", SOURCE), 1)
