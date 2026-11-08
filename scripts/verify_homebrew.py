#!/usr/bin/env python3
"""Exercise formula, bottle, upgrade and uninstall in a separate Homebrew prefix."""

import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

from release_artifacts import archive_name, digest
from render_homebrew import render


FORMULA = "obedience-corp/tap/obey-voice"
BOTTLE_ROOT = "https://github.com/Obedience-Corp/homebrew-tap/releases/download/obey-voice-"


def verify(directory, tag, workspace, brew):
    directory = directory.resolve()
    workspace = workspace.resolve()
    if workspace.exists() and any(workspace.iterdir()):
        raise ValueError(f"verification workspace must be empty: {workspace}")
    machine = (platform.system(), platform.machine())
    targets = {("Darwin", "arm64"): "macos-arm64", ("Linux", "x86_64"): "linux-amd64"}
    if machine not in targets:
        raise ValueError(f"unsupported Homebrew verification host: {machine}")
    target = targets[machine]
    if machine[0] == "Darwin" and (workspace.is_relative_to(Path("/private/tmp"))
                                   or workspace.is_relative_to(Path(tempfile.gettempdir()).resolve())):
        raise ValueError("use a workspace outside the system temporary directory, e.g. dist/homebrew-check; "
                         "Homebrew's build sandbox must protect its own checkout")
    workspace.mkdir(parents=True, exist_ok=True)
    log = workspace / "verification.log"
    env = {key: value for key, value in os.environ.items() if not key.startswith("HOMEBREW_")}
    env.update({
        "HOME": str(workspace / "user"),
        "XDG_CONFIG_HOME": str(workspace / "user/config"),
        "XDG_CACHE_HOME": str(workspace / "user/cache"),
        "HOMEBREW_CACHE": str(workspace / "brew-cache"),
        "HOMEBREW_LOGS": str(workspace / "brew-logs"),
        "HOMEBREW_TEMP": str(workspace / "build-temp"),
        "HOMEBREW_NO_AUTO_UPDATE": "1",
        "HOMEBREW_NO_ANALYTICS": "1",
        "HOMEBREW_NO_INSTALL_CLEANUP": "1",
        "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK": "1",
        "HOMEBREW_NO_ASK": "1",
    })
    for name in ("HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "HOMEBREW_TEMP"):
        Path(env[name]).mkdir(parents=True, exist_ok=True)

    def run(*args, cwd=workspace):
        command = [str(arg) for arg in args]
        print("+ " + " ".join(command), flush=True)
        result = subprocess.run(command, cwd=cwd, env=env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=1200)
        with log.open("a") as stream:
            stream.write("+ " + " ".join(command) + "\n" + result.stdout + result.stderr)
        if result.returncode:
            print(result.stdout + result.stderr, flush=True)
            raise subprocess.CalledProcessError(result.returncode, command)
        return result.stdout.strip()

    repository = subprocess.check_output([brew, "--repository"], text=True).strip()
    prefix = workspace / "brew"
    run("git", "clone", "--shared", repository, prefix)
    isolated_brew = prefix / "bin/brew"
    # Reuse the installed tool's portable interpreter without sharing writable caches.
    for name in ("portable-ruby", "bundle"):
        source = Path(repository) / "Library/Homebrew/vendor" / name
        if source.is_dir():
            shutil.copytree(source, prefix / "Library/Homebrew/vendor" / name, symlinks=True, dirs_exist_ok=True)
    tap = prefix / "Library/Taps/obedience-corp/homebrew-tap"
    formula = tap / "Formula/obey-voice.rb"
    render(directory, tag, formula)
    original = formula.read_text()
    run("git", "init", "-b", "main", tap)
    run("git", "add", "Formula/obey-voice.rb", cwd=tap)
    run("git", "-c", "user.name=Package fixture", "-c", "user.email=fixture@example.invalid",
        "commit", "-m", "Package verification fixture", cwd=tap)
    run(isolated_brew, "trust", "--tap", "obedience-corp/tap")
    run(isolated_brew, "audit", "--strict", "--formula", FORMULA)

    # Seed the verified download cache, keeping production URLs in the formula
    # and bottle. This permits pre-publication testing without a fake release.
    cached = Path(run(isolated_brew, "--cache", "--build-from-source", FORMULA))
    cached.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(directory / archive_name(tag, target), cached)
    fixture = workspace / "user"
    data_roots = (fixture / ".obey/agents/voice/obey-voice", fixture / ".cache/obey-voice")
    for relative in (".obey/agents/voice/obey-voice/config.yaml",
                     ".obey/agents/voice/obey-voice/personas/fixture.yaml",
                     ".obey/agents/voice/obey-voice/sessions/fixture.json", ".cache/obey-voice/models/fixture.bin"):
        path = fixture / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("preserve this user fixture\n")
    def snapshot():
        return {str(path): digest(path) for root in data_roots for path in root.rglob("*") if path.is_file()}

    protected = snapshot()

    def preserved():
        if snapshot() != protected:
            raise ValueError("user data changed or model/setup files were added")

    run(isolated_brew, "install", "--build-bottle", "--formula", FORMULA)
    run(isolated_brew, "test", FORMULA)
    run(prefix / "bin/obey-voice", "--version")
    preserved()
    output = workspace / "bottles"
    output.mkdir()
    run(isolated_brew, "bottle", "--json", "--no-rebuild", "--root-url", BOTTLE_ROOT + tag,
        FORMULA, cwd=output)
    bottles = list(output.glob("*.bottle.tar.gz"))
    if len(bottles) != 1:
        raise ValueError(f"expected one bottle, found {bottles}")

    # A formula revision exercises brew's actual upgrade path without inventing
    # another upstream release or changing the embedded program version.
    formula.write_text(original.replace('  license "AGPL-3.0-only"',
                                        '  revision 1\n  license "AGPL-3.0-only"'))
    run(isolated_brew, "upgrade", "--formula", FORMULA)
    run(isolated_brew, "test", FORMULA)
    preserved()
    run(isolated_brew, "uninstall", "--force", "--formula", FORMULA)
    formula.write_text(original)
    preserved()
    # Use the same formula/bottle metadata route as a public tap installation.
    # Some Homebrew releases interpret a local bottle path as a tap name.
    bottle_json = list(output.glob("*.bottle.json"))
    if len(bottle_json) != 1:
        raise ValueError(f"expected one bottle metadata file, found {bottle_json}")
    run(isolated_brew, "bottle", "--merge", "--write", "--no-commit", bottle_json[0])
    bottle_cache = Path(run(isolated_brew, "--cache", "--force-bottle", "--formula", FORMULA))
    bottle_cache.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(bottles[0], bottle_cache)
    run(isolated_brew, "install", "--force-bottle", "--formula", FORMULA)
    run(isolated_brew, "test", FORMULA)
    installed = Path(run(isolated_brew, "--prefix", FORMULA))
    receipt = json.loads((installed / "INSTALL_RECEIPT.json").read_text())
    if not receipt.get("poured_from_bottle"):
        raise ValueError("reinstall did not pour the generated bottle")
    preserved()
    run(isolated_brew, "uninstall", "--force", "--formula", FORMULA)
    preserved()
    executable = prefix / "bin/obey-voice"
    if installed.exists() or executable.exists() or executable.is_symlink():
        raise ValueError("uninstall left package-owned executable files")
    evidence = {"version": tag, "platform": target, "brew_commit": run("git", "rev-parse", "HEAD", cwd=prefix),
                "bottle": bottles[0].name, "sha256": digest(bottles[0]), "poured_from_bottle": True,
                "revision_upgrade": True, "user_data_preserved": True, "uninstalled": True}
    (output / "obey-voice.rb").write_text(original)
    (workspace / "evidence.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("--directory", type=Path, default=Path("dist/release"))
    parser.add_argument("--workspace", type=Path, required=True, help="new isolated prefix, caches and evidence")
    parser.add_argument("--brew", default="brew", help="installed Homebrew whose checkout will be cloned")
    args = parser.parse_args()
    verify(args.directory, args.tag, args.workspace, args.brew)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.SubprocessError) as error:
        raise SystemExit(f"Homebrew verification: {error}") from error
