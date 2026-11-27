#!/usr/bin/env python3
"""Isolated real TUI settings check. Fake Muse catalog only; no inference/audio."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

FAKE_MUSE = '''#!/usr/bin/env python3
import json, sys
models = [
    {"modelId": "demo-large-2", "displayLabel": "Demo Large 2", "isDefault": False},
    {"modelId": "demo-large-1", "displayLabel": "Demo Large 1", "isDefault": True},
    {"modelId": "demo-small-1", "displayLabel": "Demo Small 1", "isDefault": False},
]
for row in models:
    row.update({"providerId": "demo", "profileId": None, "isActive": False, "description": None,
                "releaseDate": None, "contextLimit": None, "outputLimit": None, "cost": None})
for line in sys.stdin:
    request = json.loads(line)
    if "id" not in request:
        continue
    if request["method"] == "initialize":
        result = {"schema": {"version": 1, "fingerprint": "demo"}}
    elif request["method"] == "model/list":
        result = {"models": models, "providerId": "demo", "profileId": None, "source": "fakeCatalog"}
    else:
        result = {}
    print(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}), flush=True)
'''


def main():
    output = Path(sys.argv[1]).resolve()
    output.mkdir(parents=True, exist_ok=True)
    binary = Path('bin/obey-voice').resolve()
    with tempfile.TemporaryDirectory(prefix='obey-muse-settings-', dir='/tmp') as temporary:
        root = Path(temporary)
        settings = root / '.obey/agents/voice/obey-voice/config.yaml'
        settings.parent.mkdir(parents=True)
        settings.write_text('''brain_provider: muse-spark
agent_name: Obey
tts_provider: kokoro
voice_workers_disabled: true
voice_tools_enabled: false
talk_hotkey_enabled: false
ollama_host: http://127.0.0.1:1
''')
        shim = root / 'bin/muse'
        shim.parent.mkdir()
        shim.write_text(FAKE_MUSE)
        shim.chmod(0o700)
        environment = {
            'HOME': temporary, 'PATH': str(shim.parent) + os.pathsep + os.environ['PATH'],
            'TERM': 'xterm-256color', 'COLORTERM': 'truecolor',
            'CLICOLOR_FORCE': '1', 'FORCE_COLOR': '1',
            'OBEY_VOICE_COLOR_PROFILE': 'ansi',
        }
        try:
            subprocess.run([str(binary), '-t', '-n'], cwd=root, env=environment, check=True)
        except KeyboardInterrupt:
            pass
        finally:
            saved = settings.read_text()
            (output / 'config.yaml').write_text(saved)


if __name__ == '__main__':
    main()
