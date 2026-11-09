#!/usr/bin/env python3
"""Create or resume a native release and verify every enabled distribution channel."""

import argparse
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import quote, urlparse
from urllib.error import HTTPError

from publish_release import validate_set
from release_artifacts import PLATFORMS, archive_name
from release_homebrew import Homebrew, reject_downgrade, stable_version
from release_service import GitHub, ReleaseError, command, public_bytes
from render_homebrew import render
from desktop_signing import SECRET_NAMES
from release_desktop import Desktop, reject_downgrade as reject_cask_downgrade


ROOT = Path(__file__).resolve().parents[1]


def load_config(path):
    config = json.loads(path.read_text())
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", config["repository"]):
        raise ReleaseError("invalid release repository")
    channels = config["channels"]
    if not channels or not any(item.get("enabled") for item in channels.values()):
        raise ReleaseError("configure at least one release channel")
    for name, item in channels.items():
        if type(item.get("enabled")) is not bool:
            raise ReleaseError(f"{name}: enabled must be a boolean")
        if not item["enabled"]:
            if not item.get("reason"):
                raise ReleaseError(f"{name}: disabled channels need an explicit reason")
            continue
        if name not in {"homebrew", "homebrew_cask", "npm"}:
            raise ReleaseError(f"enabled channel {name} has no adapter")
        if not re.fullmatch(r"[A-Za-z0-9_.-]+\.ya?ml", item.get("workflow", "")):
            raise ReleaseError(f"{name}: configure its publishing workflow")
        if name in {"homebrew", "homebrew_cask"} and item.get("repository") != "Obedience-Corp/homebrew-tap":
            raise ReleaseError("Homebrew currently targets the owning Obedience Corp tap")
        if name == "homebrew_cask":
            stable_version(item.get("since", ""))
            if not re.fullmatch(r"[A-Za-z0-9_.-]+\.ya?ml", item.get("artifact_workflow", "")):
                raise ReleaseError("homebrew_cask: configure the desktop artifact workflow")
        if name == "npm":
            if not item.get("packages") or not isinstance(item["packages"], list):
                raise ReleaseError("npm: enumerate every launcher and platform package before enabling")
            if any(not re.fullmatch(r"(?:@[a-z0-9._-]+/)?[a-z0-9._-]+", p) for p in item["packages"]):
                raise ReleaseError("npm: invalid package name")
    return config


def native_identity(github, repo, tag):
    ref = github.ref(repo, f"tags/{tag}")
    if ref:
        return github.api(f"repos/{repo}/commits/{tag}")["sha"], True
    return github.ref(repo, "heads/main"), False


def applies(channel, tag):
    return channel["enabled"] and (not channel.get("since") or
                                   stable_version(tag) >= stable_version(channel["since"]))


def check_npm_downgrade(packages, tag):
    for package in packages:
        try:
            latest = json.loads(public_bytes(f"https://registry.npmjs.org/{quote(package, safe='')}/latest"))
        except HTTPError as error:
            if error.code == 404:  # A first publication has no latest version.
                continue
            raise
        if stable_version("v" + latest["version"]) > stable_version(tag):
            raise ReleaseError(f"npm {package} already has newer {latest['version']}; refusing downgrade")


def preflight(github, config, tag, plan=False):
    stable_version(tag)
    repo = config["repository"]
    sha, exists = native_identity(github, repo, tag)
    blockers = []
    for target in {repo, *(item.get("repository", repo) for item in config["channels"].values()
                          if item["enabled"])}:
        info = github.api(f"repos/{target}")
        if not info.get("permissions", {}).get("push"):
            blockers.append(f"authenticated gh account needs write access to {target}")
    for name, channel in config["channels"].items():
        if not applies(channel, tag):
            continue
        owner = channel.get("repository", repo)
        workflow = github.api(f"repos/{owner}/actions/workflows/{channel['workflow']}", optional=True)
        if workflow is None or workflow["state"] != "active":
            blockers.append(f"{name}: publishing workflow is absent or disabled in {owner}")
        if name == "homebrew":
            current, _ = github.file(owner, "Formula/obey-voice.rb", "main")
            reject_downgrade(current, tag)
            contract, _ = github.file(owner, "packaging/obey-voice-release.json", "main")
            contract = json.loads(contract) if contract else {}
            if (contract.get("schema") != 1 or contract.get("workflow") != channel["workflow"] or
                    not {"build", "verify", "public"} <= set(contract.get("modes", []))):
                blockers.append("homebrew: merge the staged/public bottle verification workflow first")
        if name == "npm":
            check_npm_downgrade(channel["packages"], tag)
        if name == "homebrew_cask":
            current, _ = github.file(owner, "Casks/obey-voice-app.rb", "main")
            reject_cask_downgrade(current, tag)
            contract, _ = github.file(owner, "packaging/obey-voice-cask.json", "main")
            contract = json.loads(contract) if contract else {}
            if contract != {"schema": 1, "workflow": channel["workflow"], "modes": ["verify", "public"]}:
                blockers.append("homebrew_cask: merge the cask verification workflow first")
            workflow = github.api(f"repos/{repo}/actions/workflows/{channel['artifact_workflow']}", optional=True)
            if workflow is None or workflow["state"] != "active":
                blockers.append("homebrew_cask: desktop signing workflow is absent or disabled")
            source_workflow, _ = github.file(repo, f".github/workflows/{channel['artifact_workflow']}", sha)
            if source_workflow is None:
                blockers.append("homebrew_cask: release source does not contain the desktop signing workflow")
            secrets = github.api(f"repos/{repo}/actions/secrets?per_page=100")
            available = {entry["name"] for entry in secrets["secrets"]}
            missing = set(SECRET_NAMES) - available
            if missing:
                blockers.append("homebrew_cask: configure repository signing secrets: " + ", ".join(sorted(missing)))
    if not exists:
        if command("git", "status", "--porcelain"):
            blockers.append("new release requires a clean tracked/untracked working tree")
        if command("git", "rev-parse", "HEAD") != sha:
            blockers.append("new release must run from the current remote main commit")
        runs = json.loads(github.gh("run", "list", "-R", repo, "--workflow", "ci.yml", "--branch", "main",
                                   "--commit", sha, "--event", "push", "--limit", "1",
                                   "--json", "status,conclusion"))
        if not runs or runs[0] != {"status": "completed", "conclusion": "success"}:
            blockers.append("current main CI has not passed")
    result = {"version": tag, "source_sha": sha, "resume": exists,
              "channels": config["channels"], "blockers": blockers}
    if blockers and not plan:
        raise ReleaseError("; ".join(blockers))
    return result


def create_tag(github, repo, tag, sha):
    # The remote, tested main commit is the only candidate for a new tag.
    command("go", "test", "./...")
    command("just", "build")
    command("just", "release", "artifact-test")
    command("just", "release", "homebrew-test")
    if command("git", "status", "--porcelain") or command("git", "rev-parse", "HEAD") != sha:
        raise ReleaseError("checkout changed during release checks")
    if github.ref(repo, "heads/main") != sha:
        raise ReleaseError("main advanced during checks; update and retry")
    local = command("git", "tag", "--list", tag)
    if local:
        if command("git", "rev-parse", f"{tag}^{{commit}}") != sha:
            raise ReleaseError("local release tag points at a different commit")
    else:
        command("git", "tag", "-a", tag, sha, "-m", f"Obey Voice {tag}")
    command("git", "push", "origin", f"refs/tags/{tag}")


def native_release(github, repo, tag, sha, directory, report, new_tag=False):
    state = github.release(repo, tag)
    if state is None or state["isDraft"]:
        report("native", "building", source_sha=sha)
        try:
            github.workflow(repo, "release-artifact.yml", tag, sha, dispatch=not new_tag)
        except ReleaseError:
            # Homebrew qualification is an independent gate in this workflow.
            # Continue only if native publication actually succeeded; the tap
            # adapter must still pass its own exact-version lifecycle checks.
            state = github.release(repo, tag)
            if state is None or state["isDraft"]:
                raise
    state = github.release(repo, tag)
    if state is None or state["isDraft"]:
        raise ReleaseError("native release is not published")
    names = [archive_name(tag, platform) + suffix for platform in PLATFORMS for suffix in ("", ".sha256")]
    github.download_release(repo, tag, directory, names)
    files = validate_set(directory, tag)
    for path in files:
        url = f"https://github.com/{repo}/releases/download/{tag}/{path.name}"
        if public_bytes(url) != path.read_bytes():
            raise ReleaseError("native public readback differs from validated release")
    report("native", "verified", source_sha=sha, release=state["url"])


def verify_npm_package(package, tag, sha):
    url = f"https://registry.npmjs.org/{quote(package, safe='')}/{tag[1:]}"
    metadata = json.loads(public_bytes(url))
    if metadata.get("name") != package or metadata.get("version") != tag[1:] or metadata.get("gitHead") != sha:
        raise ReleaseError(f"npm {package}: registry version/source does not match release")
    dist = metadata["dist"]
    parsed = urlparse(dist["tarball"])
    if parsed.scheme != "https" or parsed.netloc != "registry.npmjs.org":
        raise ReleaseError("npm tarball must be served by the public npm registry")
    content = public_bytes(dist["tarball"])
    expected = "sha512-" + base64.b64encode(hashlib.sha512(content).digest()).decode()
    if dist.get("integrity") != expected:
        raise ReleaseError(f"npm {package}: published tarball integrity mismatch")


def execute(github, config, tag, report):
    for name, channel in config["channels"].items():
        if not channel["enabled"]:
            report(name, "not-configured", reason=channel["reason"])
        elif not applies(channel, tag):
            report(name, "not-applicable", reason=f"channel begins with {channel['since']}")
    identity = preflight(github, config, tag)
    sha, exists = identity["source_sha"], identity["resume"]
    repo = config["repository"]
    if not exists:
        report("native", "checking", source_sha=sha)
        create_tag(github, repo, tag, sha)
    with tempfile.TemporaryDirectory(prefix="obey-native-release-") as scratch:
        scratch = Path(scratch)
        native_release(github, repo, tag, sha, scratch / "native", report, new_tag=not exists)
        for name, channel in config["channels"].items():
            if not applies(channel, tag):
                continue
            if name == "homebrew":
                formula = scratch / "obey-voice.rb"
                render(scratch / "native", tag, formula)
                Homebrew(github, channel, tag, formula.read_text(), report).run()
            elif name == "homebrew_cask":
                Desktop(github, repo, channel, tag, sha, report).run()
            elif name == "npm":
                check_npm_downgrade(channel["packages"], tag)
                report(name, "publishing")
                run = github.workflow(repo, channel["workflow"], tag, sha, {"release_tag": tag})
                for package in channel["packages"]:
                    verify_npm_package(package, tag, sha)
                report(name, "verified", packages=channel["packages"], run=run)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("--plan", action="store_true", help="read-only plan, including blockers and unconfigured channels")
    parser.add_argument("--timeout", type=int, default=3600, help="seconds to wait for each workflow")
    args = parser.parse_args()
    os.chdir(ROOT)
    stable_version(args.tag)
    config = load_config(ROOT / "packaging/release-channels.json")
    github = GitHub(timeout=args.timeout)
    actual_repo = json.loads(github.gh("repo", "view", "--json", "nameWithOwner"))["nameWithOwner"]
    if actual_repo.lower() != config["repository"].lower():
        raise ReleaseError("checkout origin does not match the configured release repository")
    if args.plan:
        print(json.dumps(preflight(github, config, args.tag, plan=True), indent=2))
        return
    directory = ROOT / "dist/releases" / args.tag
    directory.mkdir(parents=True, exist_ok=True)
    # This only serializes local invocations. GitHub SHA guards, immutable assets
    # and fresh downgrade checks protect against remote concurrent releases.
    with (ROOT / "dist/releases/.lock").open("w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ReleaseError("another local release command is running") from error
        state = {"version": args.tag, "complete": False, "channels": {}}

        def save():
            temporary = directory / "status.tmp"
            temporary.write_text(json.dumps(state, indent=2) + "\n")
            temporary.replace(directory / "status.json")

        def report(channel, status, **details):
            state["channels"][channel] = {"status": status, **details}
            save()
            print(f"{channel}: {status}" + (f" — {details['reason']}" if "reason" in details else ""), flush=True)

        try:
            execute(github, config, args.tag, report)
            state["complete"] = True
        except (Exception, KeyboardInterrupt) as error:
            state["error"] = str(error) or "interrupted; rerun the same command to resume"
            raise
        finally:
            save()
        print(f"All enabled channels verified. Report: {directory / 'status.json'}")


if __name__ == "__main__":
    try:
        main()
    except (ReleaseError, ValueError, KeyError, OSError) as error:
        raise SystemExit(f"release: {error}") from error
