"""GitHub and public-readback operations shared by release channel adapters."""

import base64
import json
import subprocess
import time
from urllib.parse import quote
from urllib.request import urlopen


class ReleaseError(RuntimeError):
    pass


def command(*args, cwd=None, data=None):
    result = subprocess.run(args, cwd=cwd, input=data, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=1800)
    if result.returncode:
        raise ReleaseError(f"{args[0]} {args[1]} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def public_bytes(url):
    if not url.startswith("https://"):
        raise ReleaseError("release readback requires HTTPS")
    with urlopen(url, timeout=120) as response:
        return response.read()


class GitHub:
    def __init__(self, timeout=3600, poll=15):
        self.timeout, self.poll = timeout, poll

    def gh(self, *args):
        return command("gh", *args)

    def api(self, path, body=None, optional=False, method=None):
        args = ["gh", "api", path]
        if method:
            args.extend(["--method", method])
        if body is not None:
            args.extend(["--input", "-"])
        try:
            return json.loads(command(*args, data=json.dumps(body) if body is not None else None))
        except ReleaseError as error:
            if optional and "(HTTP 404)" in str(error):
                return None
            raise

    def file(self, repo, path, ref):
        item = self.api(f"repos/{repo}/contents/{path}?ref={quote(ref, safe='')}", optional=True)
        if item is None:
            return None, None
        return base64.b64decode(item["content"]).decode(), item["sha"]

    def ref(self, repo, ref):
        item = self.api(f"repos/{repo}/git/ref/{ref}", optional=True)
        return item["object"]["sha"] if item else None

    def release(self, repo, tag):
        # REST's tag endpoint excludes drafts. gh release view also finds drafts.
        try:
            return json.loads(self.gh("release", "view", tag, "-R", repo,
                                      "--json", "isDraft,tagName,assets,url"))
        except ReleaseError as error:
            if "release not found" in str(error).lower():
                return None
            raise

    def download_release(self, repo, tag, directory, names):
        directory.mkdir(parents=True, exist_ok=True)
        # A separate directory per readback prevents a previous file hiding a
        # missing remote asset. Callers never treat a local receipt as authority.
        for name in names:
            self.gh("release", "download", tag, "-R", repo, "--dir", str(directory),
                    "--pattern", name, "--clobber")

    def workflow(self, repo, workflow, ref, sha, inputs=None, dispatch=True):
        inputs = inputs or {}
        title = f"Obey Voice {inputs['mode']} @ {ref}" if "mode" in inputs else None
        deadline = time.monotonic() + self.timeout
        dispatched = False
        minimum_attempt = 0
        run_id = None
        while time.monotonic() < deadline:
            if run_id is None:
                runs = json.loads(self.gh("run", "list", "-R", repo, "--workflow", workflow,
                                         "--branch", ref, "--commit", sha, "--limit", "50",
                                         "--json", "databaseId,headSha,displayTitle,event"))
                matches = [run for run in runs if run["headSha"] == sha and
                           (title is None or (run["displayTitle"] == title and
                                              run["event"] == "workflow_dispatch"))]
                if matches:
                    run_id = matches[0]["databaseId"]
                    run = self.api(f"repos/{repo}/actions/runs/{run_id}")
                    if run["status"] == "completed" and run["conclusion"] != "success":
                        minimum_attempt = run["run_attempt"] + 1
                        self.gh("run", "rerun", str(run_id), "-R", repo, "--failed")
                elif not dispatched and dispatch:
                    args = ["workflow", "run", workflow, "-R", repo, "--ref", ref]
                    for key, value in inputs.items():
                        args.extend(["-f", f"{key}={value}"])
                    self.gh(*args)
                    dispatched = True
            if run_id is not None:
                run = self.api(f"repos/{repo}/actions/runs/{run_id}")
                if run["head_sha"] != sha:
                    raise ReleaseError("workflow ran a different commit; retry from current remote state")
                if run["run_attempt"] >= minimum_attempt and run["status"] == "completed":
                    if run["conclusion"] != "success":
                        raise ReleaseError(f"workflow failed: {run['html_url']}; fix it and rerun release create")
                    return run_id
            time.sleep(self.poll)
        raise ReleaseError(f"timed out waiting for {repo}/{workflow}; rerun release create to resume")

    def update_formula(self, repo, branch, text):
        path = "Formula/obey-voice.rb"
        old, blob = self.file(repo, path, branch)
        if old == text:
            return self.ref(repo, f"heads/{branch}")
        body = {"branch": branch, "message": "Update Obey Voice release formula",
                "content": base64.b64encode(text.encode()).decode()}
        if blob:
            body["sha"] = blob
        result = self.api(f"repos/{repo}/contents/{path}", body, method="PUT")
        return result["commit"]["sha"]

    def pr(self, repo, branch, tag):
        prs = json.loads(self.gh("pr", "list", "-R", repo, "--head", branch, "--state", "open",
                                "--json", "number,isDraft,url"))
        if prs:
            return prs[0]
        self.gh("pr", "create", "-R", repo, "--head", branch, "--base", "main", "--draft",
                "--title", f"Release Obey Voice {tag}", "--body",
                "Automated native formula and bottle update. The release command verifies both "
                "platforms and public bottle URLs before marking this PR ready and merging. "
                "Physical voice acceptance remains the release operator's responsibility.")
        return self.pr(repo, branch, tag)

    def merge(self, repo, pr, sha):
        if pr["isDraft"]:
            self.gh("pr", "ready", str(pr["number"]), "-R", repo)
        # Do not bypass reviews, branch protection, or a changed PR head.
        self.gh("pr", "merge", str(pr["number"]), "-R", repo, "--squash", "--match-head-commit", sha)
        state = json.loads(self.gh("pr", "view", str(pr["number"]), "-R", repo, "--json", "state"))
        if state["state"] != "MERGED":
            raise ReleaseError(f"tap PR awaits repository review/merge: {pr['url']}; rerun after approval")
