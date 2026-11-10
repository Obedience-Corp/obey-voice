"""Publish verified desktop assets and promote their Homebrew cask."""

import base64
import json
from pathlib import Path
import re
import tempfile

from desktop_artifacts import archive_name, validate
from desktop_signing import signing_mode
from release_homebrew import stable_version
from release_service import ReleaseError, public_bytes


CASK = "Casks/obey-voice-app.rb"


def names(tag):
    name = archive_name(tag)
    return [name, name + ".sha256", name.removesuffix(".zip") + ".json"]


def render(tag, checksum, signing="notarized"):
    stable_version(tag)
    if not re.fullmatch(r"[a-f0-9]{64}", checksum):
        raise ReleaseError("invalid desktop checksum")
    signing_mode({"signing": signing})
    notice = ""
    if signing == "ad-hoc":
        notice = ("    This app is ad-hoc signed and is not notarized by Apple.\n"
                  "    After attempting to open it, macOS may require approval in\n"
                  "    System Settings > Privacy & Security > Open Anyway.\n")
    return '''# Signing mode: %s
cask "obey-voice-app" do
  version "%s"
  sha256 "%s"

  url "https://github.com/Obedience-Corp/obey-voice/releases/download/v#{version}/obey-voice-app-v#{version}-macos-arm64.zip"
  name "Obey Voice Agent"
  desc "Native desktop interface for Obey Voice"
  homepage "https://github.com/Obedience-Corp/obey-voice"

  depends_on arch: :arm64
  depends_on macos: :sequoia

  app "Obey Voice Agent.app"

  uninstall quit: "com.obedience.voice-agent"

  caveats <<~EOS
%s    Choose providers and download speech models during first-launch setup.
    The app includes its own engine and native libraries; the CLI formula is separate.
    Settings, personas, sessions, and model caches are retained on uninstall.
  EOS
end
''' % (signing, tag[1:], checksum, notice)


def reject_downgrade(text, tag):
    if text is None:
        return
    match = re.search(r'^  version "([0-9]+\.[0-9]+\.[0-9]+)"$', text, re.M)
    if not match or stable_version("v" + match[1]) > stable_version(tag):
        raise ReleaseError("desktop cask version is unknown or newer; refusing downgrade")


class Desktop:
    def __init__(self, github, repo, config, tag, source, report):
        self.github, self.repo, self.config = github, repo, config
        self.tag, self.source, self.report = tag, source, report
        self.tap = config["repository"]
        self.branch = f"release/obey-voice-app-{tag}"
        self.signing = signing_mode(config)

    def current(self):
        text, _ = self.github.file(self.tap, CASK, "main")
        reject_downgrade(text, self.tag)
        return text

    def assets(self, scratch):
        state = self.github.release(self.repo, self.tag)
        if state is None or state["isDraft"]:
            raise ReleaseError("desktop promotion requires the published native release")
        expected = set(names(self.tag))
        existing = {asset["name"] for asset in state["assets"]}
        if expected <= existing:
            self.github.download_release(self.repo, self.tag, scratch, names(self.tag))
        else:
            self.report("homebrew_cask", "building-desktop", signing=self.signing)
            run = self.github.workflow(self.repo, self.config["artifact_workflow"], self.tag, self.source)
            self.github.gh("run", "download", str(run), "-R", self.repo,
                           "--name", "obey-voice-desktop", "--dir", str(scratch))
            validate(scratch, self.tag, self.source, signing=self.signing)
            # Resume partial uploads only when they match this verified build.
            # Never overwrite published bytes, even if a new notarization differs.
            for name in names(self.tag):
                if name in existing:
                    with tempfile.TemporaryDirectory() as check:
                        self.github.download_release(self.repo, self.tag, Path(check), [name])
                        if (Path(check) / name).read_bytes() != (scratch / name).read_bytes():
                            raise ReleaseError("published desktop asset differs; refusing replacement")
                else:
                    self.github.gh("release", "upload", self.tag, "-R", self.repo, str(scratch / name))
        receipt = validate(scratch, self.tag, self.source, signing=self.signing)
        for name in names(self.tag):
            if public_bytes(f"https://github.com/{self.repo}/releases/download/{self.tag}/{name}") != (scratch / name).read_bytes():
                raise ReleaseError("public desktop readback differs from verified bytes")
        return receipt

    def update_cask(self, desired):
        if self.github.ref(self.tap, f"heads/{self.branch}") is None:
            self.github.api(f"repos/{self.tap}/git/refs", {
                "ref": f"refs/heads/{self.branch}", "sha": self.github.ref(self.tap, "heads/main")})
        old, blob = self.github.file(self.tap, CASK, self.branch)
        if old == desired:
            return self.github.ref(self.tap, f"heads/{self.branch}")
        body = {"branch": self.branch, "message": f"Update Obey Voice desktop to {self.tag}",
                "content": base64.b64encode(desired.encode()).decode()}
        if blob:
            body["sha"] = blob
        return self.github.api(f"repos/{self.tap}/contents/{CASK}", body, method="PUT")["commit"]["sha"]

    def pull_request(self):
        prs = json.loads(self.github.gh("pr", "list", "-R", self.tap, "--head", self.branch,
                                       "--state", "open", "--json", "number,isDraft,url"))
        if prs:
            return prs[0]
        self.github.gh("pr", "create", "-R", self.tap, "--head", self.branch, "--base", "main",
                       "--title", f"Release Obey Voice desktop {self.tag}", "--body",
                       f"Install the verified {self.signing} desktop archive. "
                       "The release coordinator verifies public asset checksums, cask installation, "
                       "signature, expected Gatekeeper behavior, upgrade/reinstall and data retention before merge. "
                       + ("This ad-hoc signed build is not notarized; users may need Open Anyway approval."
                          if self.signing == "ad-hoc" else "Developer ID/notarization and Gatekeeper acceptance are required."))
        return self.pull_request()

    def run(self):
        self.current()
        with tempfile.TemporaryDirectory(prefix="obey-desktop-release-") as scratch:
            receipt = self.assets(Path(scratch))
            desired = render(self.tag, receipt["sha256"], self.signing)
        current = self.current()
        sha = self.update_cask(desired)
        pr = self.pull_request() if current != desired else None
        self.report("homebrew_cask", "verifying-cask")
        self.github.workflow(self.tap, self.config["workflow"], self.branch, sha, {"mode": "verify"})
        self.current()
        if pr:
            self.github.merge(self.tap, pr, sha)
        if self.current() != desired:
            raise ReleaseError("desktop cask awaits review/merge; rerun release create afterward")
        main = self.github.ref(self.tap, "heads/main")
        run = self.github.workflow(self.tap, self.config["workflow"], "main", main, {"mode": "public"})
        self.report("homebrew_cask", "verified", version=self.tag, signing=self.signing,
                    gatekeeper_approval_required=self.signing == "ad-hoc",
                    run=f"https://github.com/{self.tap}/actions/runs/{run}")
