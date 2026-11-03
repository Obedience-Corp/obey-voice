#!/usr/bin/env python3
"""Check tracked media coverage and byte-bound public-release review records."""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys


MEDIA_SUFFIXES = {
    ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".ico",
    ".icns", ".svg", ".wav", ".wave", ".mp3", ".flac", ".ogg", ".m4a",
    ".aac", ".mp4", ".webm", ".mov", ".ttf", ".otf", ".woff", ".woff2",
    ".srt", ".vtt", ".pdf",
}
# Textual evidence requiring review even though it is not a media extension.
REVIEW_PATHS = {"docs/benchmarks/t3b/transcripts.json"}


def tracked_paths(root):
    result = subprocess.run(
        ["git", "ls-files", "-z"], cwd=root, check=True, capture_output=True,
    )
    return set(result.stdout.decode("utf-8").rstrip("\0").split("\0")) - {""}


def check(root, manifest, tracked):
    errors = []
    blocked = []
    entries = manifest.get("assets") if isinstance(manifest, dict) else None
    if not isinstance(entries, list):
        return ["inventory must contain an assets array"], []
    indexed = set()
    for entry in entries:
        if not isinstance(entry, dict) or not isinstance(entry.get("path"), str):
            errors.append("asset entry must be an object with a string path")
            continue
        name = entry["path"]
        path = PurePosixPath(name)
        if not name or path.is_absolute() or ".." in path.parts or str(path) != name:
            errors.append(f"invalid asset path: {name}")
            continue
        if name in indexed:
            errors.append(f"duplicate asset: {name}")
        indexed.add(name)
        if name not in tracked:
            errors.append(f"asset is not tracked: {name}")
            continue
        asset = root / name
        if asset.is_symlink() or not asset.is_file():
            errors.append(f"asset must be a regular file: {name}")
            continue
        digest = entry.get("sha256")
        if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            errors.append(f"missing or invalid sha256: {name}")
        elif hashlib.sha256(asset.read_bytes()).hexdigest() != digest:
            errors.append(f"asset changed; review record must be refreshed: {name}")
        for field in ("source", "disposition", "content_review"):
            if not isinstance(entry.get(field), str) or not entry[field].strip():
                errors.append(f"missing {field}: {name}")
        status = entry.get("public_release")
        if status == "blocked":
            blocked.append(name)
        elif status == "cleared":
            review = entry.get("review", {})
            if not isinstance(review, dict):
                review = {}
            for field in ("reviewer", "reviewed_at", "content_evidence", "rights_evidence"):
                if not isinstance(review.get(field), str) or not review[field].strip():
                    errors.append(f"cleared asset lacks review.{field}: {name}")
            if entry.get("disposition") != "retain_public":
                errors.append(f"cleared asset must have retain_public disposition: {name}")
            if entry.get("content_review") != "complete":
                errors.append(f"cleared asset must have complete content_review: {name}")
        else:
            errors.append(f"invalid public_release status: {name}")
    required = {
        name for name in tracked
        if PurePosixPath(name).suffix.lower() in MEDIA_SUFFIXES or name in REVIEW_PATHS
    }
    errors.extend(f"unlisted tracked media: {name}" for name in sorted(required - indexed))
    return errors, blocked


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--require-cleared", action="store_true",
                        help="fail if any inventoried asset is still blocked")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    try:
        manifest = json.loads((root / "docs/media-review.json").read_text())
        errors, blocked = check(root, manifest, tracked_paths(root))
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"media review failed: {exc}", file=sys.stderr)
        return 1
    for error in errors:
        print(error, file=sys.stderr)
    entries = manifest.get("assets", []) if isinstance(manifest, dict) else []
    count = len(entries) if isinstance(entries, list) else 0
    print(f"Media inventory: {count} records; "
          f"{len(errors)} errors; {len(blocked)} blocked for publication.")
    if args.require_cleared and blocked:
        print("Public media gate failed; resolve blocked records in docs/media-review.json.",
              file=sys.stderr)
    return int(bool(errors or (args.require_cleared and blocked)))


if __name__ == "__main__":
    sys.exit(main())
