"""Authenticated notice metadata stays confined to GitHub's HTTPS API."""
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError
from urllib.request import Request

import desktop_notices as notices


class DesktopNoticeTests(unittest.TestCase):
    def test_api_token_selection_and_anonymous_local_use(self):
        for env, expected in (({}, None), ({"GH_TOKEN": "fixture"}, "Bearer fixture"),
                              ({"GITHUB_TOKEN": "fallback"}, "Bearer fallback"),
                              ({"GH_TOKEN": "first", "GITHUB_TOKEN": "second"}, "Bearer first")):
            with self.subTest(env=env), patch.dict(os.environ, env, clear=True), patch.object(notices, "build_opener") as opener:
                opener.return_value.open.return_value = io.BytesIO(b'{"tree": []}')
                self.assertEqual(notices.github_tree("owner/repo", "a" * 40), {"tree": []})
                args, kwargs = opener.return_value.open.call_args
                self.assertEqual(args[0].get_header("Authorization"), expected)
                self.assertTrue(args[0].full_url.startswith("https://api.github.com/"))
                self.assertEqual(kwargs["timeout"], 60)

    def test_api_redirect_cannot_forward_token_outside_api(self):
        request = Request("https://api.github.com/repos/old/repo", headers={"Authorization": "Bearer fixture"})
        handler = notices.GitHubAPIRedirect()
        for target in ("https://raw.githubusercontent.com/notice", "http://api.github.com/notice", "https://api.github.com.evil.test/notice"):
            with self.subTest(target=target), self.assertRaises(HTTPError):
                handler.redirect_request(request, None, 302, "redirect", {}, target)
        redirected = handler.redirect_request(request, None, 301, "renamed", {}, "https://api.github.com/repos/new/repo")
        self.assertEqual(redirected.get_header("Authorization"), "Bearer fixture")

    def test_notice_files_are_still_fetched_without_credentials(self):
        with tempfile.TemporaryDirectory() as scratch:
            directory = Path(scratch) / "crate"
            target = Path(scratch) / "notices"
            directory.mkdir()
            target.mkdir()
            (directory / ".cargo_vcs_info.json").write_text(json.dumps({"git": {"sha1": "a" * 40}}))
            tree = {"tree": [{"path": "LICENSE", "type": "blob"}]}
            with patch.dict(os.environ, {"GH_TOKEN": "fixture"}), patch.object(notices, "github_tree", return_value=tree), patch.object(notices, "urlopen", return_value=io.BytesIO(b"license text")) as raw:
                notices.upstream_notices({"name": "fixture", "repository": "https://github.com/owner/repo"}, directory, target, {})
                self.assertIsInstance(raw.call_args.args[0], str)
                self.assertTrue(raw.call_args.args[0].startswith("https://raw.githubusercontent.com/"))
                self.assertEqual((target / "upstream-0-LICENSE").read_bytes(), b"license text")

    def test_api_rate_limit_remains_fatal(self):
        with tempfile.TemporaryDirectory() as scratch:
            directory = Path(scratch)
            (directory / ".cargo_vcs_info.json").write_text(json.dumps({"git": {"sha1": "a" * 40}}))
            error = HTTPError("https://api.github.com/", 403, "rate limited", {}, None)
            with patch.object(notices, "github_tree", side_effect=error), self.assertRaises(HTTPError):
                notices.upstream_notices({"name": "fixture", "repository": "https://github.com/owner/repo"}, directory, directory, {})


if __name__ == "__main__":
    unittest.main()
