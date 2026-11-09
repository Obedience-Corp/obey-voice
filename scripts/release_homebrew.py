"""Promote the native release through the owning Homebrew tap."""

import json
from pathlib import Path
import re
import shutil
import tempfile

from release_artifacts import digest
from release_service import ReleaseError, public_bytes


TAGS = ("arm64_sequoia", "x86_64_linux")
FORMULA = "obedience-corp/tap/obey-voice"


def stable_version(tag):
    match = re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", tag)
    if not match:
        raise ReleaseError("channel releases require a stable vMAJOR.MINOR.PATCH tag")
    return tuple(map(int, match.groups()))


def reject_downgrade(formula, tag):
    if not formula:
        return
    match = re.search(r"/releases/download/(v[0-9]+\.[0-9]+\.[0-9]+)/", formula)
    if not match:
        raise ReleaseError("cannot establish the existing tap version; refusing to replace it")
    if stable_version(match[1]) > stable_version(tag):
        raise ReleaseError(f"tap already contains newer {match[1]}; refusing downgrade to {tag}")


def without_bottles(formula):
    return re.sub(r"\n  bottle do\n.*?\n  end\n", "\n", formula, flags=re.S)


def normalized(formula):
    return "\n".join(line.strip() for line in formula.splitlines() if line.strip())


def validate_bottles(directory, tag, repo):
    """Require an exact platform pair, matching provenance and safe public names."""
    version = tag[1:]
    result = {}
    commits = set()
    root_url = f"https://github.com/{repo}/releases/download/obey-voice-{tag}"
    for target in TAGS:
        meta_name = f"obey-voice--{version}.{target}.bottle.json"
        meta = json.loads((directory / meta_name).read_text())
        if set(meta) != {FORMULA}:
            raise ReleaseError("unexpected formula in bottle metadata")
        data = meta[FORMULA]
        formula, bottle = data["formula"], data["bottle"]
        commit = formula["tap_git_revision"]
        if not re.fullmatch(r"[0-9a-f]{40}", commit):
            raise ReleaseError("invalid bottle source commit")
        commits.add(commit)
        if (formula["pkg_version"] != version or formula["tap_git_path"] != "Formula/obey-voice.rb"
                or formula["tap_git_remote"].removesuffix(".git") != f"https://github.com/{repo}"
                or bottle["root_url"] != root_url or bottle["rebuild"] != 0
                or set(bottle["tags"]) != {target}):
            raise ReleaseError("bottle version, origin, rebuild or platform does not match release")
        entry = bottle["tags"][target]
        public_name = f"obey-voice-{version}.{target}.bottle.tar.gz"
        local_name = f"obey-voice--{version}.{target}.bottle.tar.gz"
        if entry["filename"] != public_name or entry["local_filename"] != local_name:
            raise ReleaseError("unexpected bottle filename")
        if bottle["cellar"] not in ("any", "any_skip_relocation"):
            raise ReleaseError("bottle is not relocatable across supported Homebrew prefixes")
        if not re.fullmatch(r"[0-9a-f]{64}", entry["sha256"]):
            raise ReleaseError("invalid bottle checksum")
        archive = directory / public_name
        if not archive.exists():
            archive = directory / local_name
        if digest(archive) != entry["sha256"]:
            raise ReleaseError(f"bottle checksum mismatch: {target}")
        result[target] = {**entry, "cellar": bottle["cellar"], "archive": archive,
                          "metadata": directory / meta_name}
    if len(commits) != 1:
        raise ReleaseError("bottles were built from different tap commits")
    return result, commits.pop()


def bottle_formula(base, entries, tag, repo):
    lines = ["", "  bottle do",
             f'    root_url "https://github.com/{repo}/releases/download/obey-voice-{tag}"']
    for target in TAGS:
        entry = entries[target]
        padding = " " if target == "arm64_sequoia" else "  "
        lines.append(f'    sha256 cellar: :{entry["cellar"]}, {target}:{padding}"{entry["sha256"]}"')
    lines.append("  end\n")
    base = without_bottles(base)
    marker = '  license "AGPL-3.0-only"\n'
    if base.count(marker) != 1:
        raise ReleaseError("cannot locate formula license stanza")
    return base.replace(marker, marker + "\n".join(lines))


def release_names(tag):
    return [f"obey-voice{separator}{tag[1:]}.{target}.bottle.{extension}"
            for target in TAGS for separator, extension in (("-", "tar.gz"), ("--", "json"))]


class Homebrew:
    def __init__(self, github, config, tag, base, report):
        self.github, self.config, self.tag, self.base, self.report = github, config, tag, base, report
        self.repo = config["repository"]
        self.workflow = config["workflow"]
        self.branch = f"release/obey-voice-{tag}"
        self.release_tag = f"obey-voice-{tag}"

    def current(self):
        text, _ = self.github.file(self.repo, "Formula/obey-voice.rb", "main")
        reject_downgrade(text, self.tag)
        return text

    def verify_source(self, entries, source):
        text, _ = self.github.file(self.repo, "Formula/obey-voice.rb", source)
        if text is None or normalized(without_bottles(text)) != normalized(self.base):
            raise ReleaseError("bottles were not built from the formula for these verified native assets")

    def readback(self, directory, public=False):
        self.github.download_release(self.repo, self.release_tag, directory, release_names(self.tag))
        entries, source = validate_bottles(directory, self.tag, self.repo)
        self.verify_source(entries, source)
        if public:
            for entry in entries.values():
                url = (f"https://github.com/{self.repo}/releases/download/"
                       f"{self.release_tag}/{entry['filename']}")
                if public_bytes(url) != entry["archive"].read_bytes():
                    raise ReleaseError("public bottle readback differs from staged bytes")
        return entries

    def stage(self, directory, entries, source):
        release = self.github.release(self.repo, self.release_tag)
        if release and not release["isDraft"]:
            raise ReleaseError("refusing to replace an already published bottle release")
        if release is None:
            self.github.gh("release", "create", self.release_tag, "-R", self.repo, "--draft",
                           "--target", source, "--title", f"Obey Voice {self.tag} bottles",
                           "--notes", f"Native bottles from tap {source}; promoted by just release create.")
            release = self.github.release(self.repo, self.release_tag)
        existing = {asset["name"] for asset in release["assets"]}
        upload = directory / "upload"
        upload.mkdir()
        for entry in entries.values():
            shutil.copyfile(entry["archive"], upload / entry["filename"])
            shutil.copyfile(entry["metadata"], upload / entry["metadata"].name)
        # Resume partial uploads, but never clobber differing bytes even in a draft.
        for path in upload.iterdir():
            if path.name in existing:
                with tempfile.TemporaryDirectory() as check:
                    check = Path(check)
                    self.github.download_release(self.repo, self.release_tag, check, [path.name])
                    if digest(check / path.name) != digest(path):
                        raise ReleaseError("draft contains different bytes; inspect it before retrying")
            else:
                self.github.gh("release", "upload", self.release_tag, "-R", self.repo, str(path))

    def run(self):
        current = self.current()
        branch_sha = self.github.ref(self.repo, f"heads/{self.branch}")
        if branch_sha is None:
            main = self.github.ref(self.repo, "heads/main")
            self.github.api(f"repos/{self.repo}/git/refs", {"ref": f"refs/heads/{self.branch}", "sha": main})
        with tempfile.TemporaryDirectory(prefix="obey-homebrew-release-") as scratch:
            scratch = Path(scratch)
            release = self.github.release(self.repo, self.release_tag)
            complete = release and set(release_names(self.tag)) <= {asset["name"] for asset in release["assets"]}
            if complete:
                entries = self.readback(scratch / "existing", public=not release["isDraft"])
            else:
                if release and not release["isDraft"]:
                    raise ReleaseError("published bottle release is incomplete; cannot mutate it")
                sha = self.github.update_formula(self.repo, self.branch, self.base)
                pr = self.github.pr(self.repo, self.branch, self.tag)
                self.report("homebrew", "building", pr=pr["url"])
                run = self.github.workflow(self.repo, self.workflow, self.branch, sha, {"mode": "build"})
                artifacts = scratch / "build"
                self.github.gh("run", "download", str(run), "-R", self.repo, "--dir", str(artifacts))
                combined = scratch / "combined"
                combined.mkdir()
                for target, platform in zip(TAGS, ("macos-arm64", "linux-amd64")):
                    root = artifacts / f"obey-voice-bottle-{platform}"
                    evidence = json.loads((root / "evidence.json").read_text())
                    if (evidence["tap_commit"] != sha or evidence["version"] != self.tag[1:] or
                            not all(evidence.get(key) is True for key in
                                    ("public_native_download", "poured_from_bottle", "revision_upgrade",
                                     "user_data_preserved", "uninstalled"))):
                        raise ReleaseError("bottle workflow evidence is incomplete or for another commit")
                    for extension in ("json", "tar.gz"):
                        name = f"obey-voice--{self.tag[1:]}.{target}.bottle.{extension}"
                        shutil.copyfile(root / "bottles" / name, combined / name)
                entries, source = validate_bottles(combined, self.tag, self.repo)
                if source != sha:
                    raise ReleaseError("bottle artifacts do not match the requested workflow commit")
                self.verify_source(entries, source)
                self.stage(scratch, entries, source)
                entries = self.readback(scratch / "staged")
            desired = bottle_formula(self.base, entries, self.tag, self.repo)
            self.current()  # Another release may have advanced while builders ran.
            sha = self.github.update_formula(self.repo, self.branch, desired)
            pr = None
            if normalized(current or "") != normalized(desired):
                pr = self.github.pr(self.repo, self.branch, self.tag)
            self.report("homebrew", "verifying-staged-bottles")
            self.github.workflow(self.repo, self.workflow, self.branch, sha,
                                 {"mode": "verify", "release_tag": self.release_tag})
            self.current()
            release = self.github.release(self.repo, self.release_tag)
            if release["isDraft"]:
                self.github.gh("release", "edit", self.release_tag, "-R", self.repo,
                               "--draft=false", "--latest=false")
            if self.github.release(self.repo, self.release_tag)["isDraft"]:
                raise ReleaseError("bottle release remains draft")
            self.readback(scratch / "public", public=True)
            self.report("homebrew", "published-bottles", release=release["url"])
            self.current()
            if pr:
                self.github.merge(self.repo, pr, sha)
            main = self.github.ref(self.repo, "heads/main")
            live = self.current()
            if normalized(live or "") != normalized(desired):
                raise ReleaseError("tap main does not contain the verified formula; retry after review")
            run = self.github.workflow(self.repo, self.workflow, "main", main, {"mode": "public"})
            self.report("homebrew", "verified", version=self.tag,
                        run=f"https://github.com/{self.repo}/actions/runs/{run}")
