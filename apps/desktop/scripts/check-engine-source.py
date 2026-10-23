#!/usr/bin/env python3
"""Fail if bridge dependency resolution drifts away from the enclosing engine."""
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
expected = root.parent.parent
for workspace in (None, "off"):
    env = os.environ.copy()
    if workspace:
        env["GOWORK"] = workspace
    module = json.loads(subprocess.check_output(
        ["go", "list", "-m", "-json", "github.com/Obedience-Corp/obey-voice"],
        cwd=root / "bridge", env=env, text=True))
    actual = Path(module["Dir"]).resolve()
    if actual != expected:
        raise SystemExit(f"Bridge resolves engine to {actual}, expected {expected}")
print(f"Bridge uses this checkout: {expected} (including GOWORK=off)")
