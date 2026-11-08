#!/usr/bin/env python3
"""Render the Obedience Corp tap formula from a complete verified native release."""

import argparse
from pathlib import Path
import re

from release_artifacts import PLATFORMS, RELEASE_TAG, archive_name, digest, verify


TEMPLATE = Path(__file__).resolve().parents[1] / "packaging/homebrew/obey-voice.rb.template"
RELEASE_ROOT = "https://github.com/Obedience-Corp/obey-voice/releases/download"


def render(directory, tag, destination):
    if not RELEASE_TAG.fullmatch(tag):
        raise ValueError("Homebrew packaging requires vMAJOR.MINOR.PATCH[-PRERELEASE]")
    replacements = {"VERSION": tag[1:]}
    for platform in PLATFORMS:
        archive = directory / archive_name(tag, platform)
        verify(archive, tag, platform)
        key = "MACOS" if platform == "macos-arm64" else "LINUX"
        replacements[key + "_URL"] = f"{RELEASE_ROOT}/{tag}/{archive.name}"
        replacements[key + "_SHA"] = digest(archive)
    formula = TEMPLATE.read_text()
    for key, value in replacements.items():
        formula = formula.replace(f"@@{key}@@", value)
    if re.search(r"@@[A-Z_]+@@", formula):
        raise ValueError("unresolved Homebrew template marker")
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(formula)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("--directory", type=Path, default=Path("dist/release"))
    parser.add_argument("--output", type=Path, default=Path("dist/homebrew/Formula/obey-voice.rb"))
    args = parser.parse_args()
    render(args.directory, args.tag, args.output)
    print(args.output)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError) as error:
        raise SystemExit(f"Homebrew formula: {error}") from error
