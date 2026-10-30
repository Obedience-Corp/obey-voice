#!/usr/bin/env python3
"""Enforce the 500-line limit for hand-maintained repository text files."""
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
MAX_LINES = 500
# Machine-generated dependency records must remain intact for reproducible builds.
GENERATED = {"Cargo.lock", "bridge/go.sum"}
# The standard AGPL text must remain verbatim, including its line breaks.
VERBATIM_LEGAL = {"LICENSE"}


def main():
    paths = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        cwd=ROOT,
    ).decode().split("\0")
    failures = []
    checked = 0
    for name in sorted(set(paths) - GENERATED - VERBATIM_LEGAL - {""}):
        path = ROOT / name
        if not path.is_file() or path.is_symlink():
            continue
        try:
            content = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        if "\0" in content:
            continue
        checked += 1
        count = len(content.splitlines())
        if count > MAX_LINES:
            failures.append(f"{name}: {count} lines (maximum {MAX_LINES})")
    if failures:
        print("Split oversized files by responsibility:\n" + "\n".join(failures), file=sys.stderr)
        return 1
    print(f"File size: {checked} hand-maintained text files are at most {MAX_LINES} lines.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
