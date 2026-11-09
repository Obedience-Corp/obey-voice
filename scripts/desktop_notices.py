"""Carry native and Rust dependency notices with the public desktop bundle."""

import json
import re
from pathlib import Path
import shutil
import subprocess
import tempfile
from urllib.request import urlopen
from urllib.error import HTTPError


def is_notice(name):
    name = name.upper()
    return any(word in name for word in ("LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT")) or name.startswith(("OFL", "UFL"))


def upstream_notices(package, directory, target, cache):
    """Some workspace crates omit licenses from .crate; use their exact VCS commit."""
    vcs = json.loads((directory / ".cargo_vcs_info.json").read_text())
    sha = vcs["git"]["sha1"]
    match = re.match(r"https?://github.com/([\w.-]+/[\w.-]+)(?:/|$)", package["repository"])
    if not match or not re.fullmatch(r"[a-f0-9]{40}", sha):
        raise ValueError(f"cannot locate pinned upstream notices for {package['name']}")
    repository = match[1].removesuffix(".git")
    key = repository, sha
    if key not in cache:
        try:
            with urlopen(f"https://api.github.com/repos/{repository}/git/trees/{sha}?recursive=1", timeout=60) as response:
                tree = json.load(response)
        except HTTPError as error:
            if error.code not in (404, 422):
                raise
            # Some published crates reference commits no longer accessible in
            # the upstream repository. Keep the exact registry source instead.
            tree = {"tree": []}
        if tree.get("truncated"):
            raise ValueError(f"upstream license tree is truncated: {repository}@{sha}")
        cache[key] = tree["tree"]
    crate_path = Path(vcs.get("path_in_vcs", ""))
    parents = {crate_path, *crate_path.parents}
    found = []
    for entry in cache[key]:
        path = Path(entry["path"])
        if entry["type"] != "blob" or not is_notice(path.name):
            continue
        if path.parent not in parents and crate_path not in path.parents:
            continue
        url = f"https://raw.githubusercontent.com/{repository}/{sha}/{path.as_posix()}"
        if url not in cache:
            with urlopen(url, timeout=60) as response:
                cache[url] = response.read()
        data = cache[url]
        (target / f"upstream-{len(found)}-{path.name}").write_bytes(data)
        found.append(url)
    if not found:
        # A few older crates declare their license only in Cargo.toml and
        # provide no standalone notice even upstream. Preserve the complete
        # published source, including original manifest and any source headers.
        shutil.copytree(directory, target / "upstream-crate-source")
    (target / "upstream-sources.json").write_text(json.dumps(found, indent=2) + "\n")


def collect(root, bundle):
    resources = bundle / "Contents/Resources"
    module = json.loads(subprocess.check_output(
        ["go", "list", "-m", "-json", "github.com/k2-fsa/sherpa-onnx-go-macos"], cwd=root))
    with tempfile.TemporaryDirectory(prefix="obey-desktop-notices-") as scratch:
        stage = Path(scratch)
        (stage / "lib").symlink_to(bundle / "Contents/Frameworks", target_is_directory=True)
        subprocess.run(["bash", "scripts/package-macos-notices.sh", str(stage), module["Dir"]],
                       cwd=root, check=True)
        for name in ("LICENSE", "NOTICE"):
            shutil.copyfile(stage / name, resources / name)
        shutil.copytree(stage / "licenses", resources / "licenses")
    metadata = json.loads(subprocess.check_output(
        ["cargo", "metadata", "--locked", "--format-version", "1",
         "--filter-platform", "aarch64-apple-darwin"], cwd=root / "apps/desktop"))
    nodes = {node["id"]: node for node in metadata["resolve"]["nodes"]}
    selected, queue = set(), [metadata["resolve"]["root"]]
    while queue:
        identifier = queue.pop()
        if identifier in selected:
            continue
        selected.add(identifier)
        queue.extend(dep["pkg"] for dep in nodes[identifier]["deps"]
                     if any(kind["kind"] is None for kind in dep["dep_kinds"]))
    inventory, upstream = [], {}
    for package in metadata["packages"]:
        if package["id"] not in selected or package["source"] is None:
            continue
        directory = Path(package["manifest_path"]).parent
        notices = {p for p in directory.rglob("*") if p.is_file() and
                   (is_notice(p.name) or (p.suffix == ".txt" and "fonts" in p.parts))}
        if package["license_file"]:
            notices.add(directory / package["license_file"])
        for child in ("licenses", "license"):
            if (directory / child).is_dir():
                notices.update(p for p in (directory / child).rglob("*") if p.is_file())
        target = resources / "licenses/rust" / f"{package['name']}-{package['version']}"
        target.mkdir(parents=True)
        # Root/workspace licenses may be omitted even when a bundled font has
        # its own notice. Include upstream ancestors when the crate has no root notice.
        if not any(p.parent == directory for p in notices):
            upstream_notices(package, directory, target, upstream)
        for index, notice in enumerate(sorted(notices)):
            shutil.copyfile(notice, target / f"{index}-{notice.name}")
        inventory.append({key: package[key] for key in ("name", "version", "license")})
    (resources / "licenses/rust-dependencies.json").write_text(json.dumps(inventory, indent=2) + "\n")
