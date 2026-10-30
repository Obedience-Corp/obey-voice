"""Retain public-install provenance and fixture assertions in the CI job summary."""
import json
import os
from pathlib import Path

root = Path('.test-output/obey-daemon')
summary = ['## Released Obey integration', '']
if not root.exists():
    summary += ['Evidence was not produced; inspect the preceding setup/test step.']
else:
    for name in ('sdk-module.txt', 'obey-version.txt'):
        if (root / name).exists():
            summary += ['```text', (root / name).read_text().strip(), '```', '']
    if (root / 'packages.json').exists():
        document = json.loads((root / 'packages.json').read_text())
        for package in document.get('data', {}).get('packages', []):
            summary.append(f"- Installed `{package.get('package_id')}` version `{package.get('version')}` from `{package.get('source')}`.")
        summary.append('')
    for name in ('failure-paths.log', 'real-daemon.log'):
        if (root / name).exists():
            summary += [f'### {name}', '', '```text', (root / name).read_text().strip(), '```', '']
output = '\n'.join(summary) + '\n'
print(output)
with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as destination:
    destination.write(output)
