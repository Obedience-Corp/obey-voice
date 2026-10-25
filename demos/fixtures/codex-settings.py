#!/usr/bin/env python3
"""Isolated real TUI settings check. Metadata fixture only; no inference/audio."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    output = Path(sys.argv[1]).resolve()
    output.mkdir(parents=True, exist_ok=True)
    binary = Path('bin/obey-voice').resolve()
    with tempfile.TemporaryDirectory(prefix='obey-codex-settings-') as temporary:
        root = Path(temporary)
        settings = root / '.obey/agents/voice/obey-voice/config.yaml'
        settings.parent.mkdir(parents=True)
        settings.write_text('''brain_provider: codex
codex_model: fixture-model
agent_name: Obey
tts_provider: kokoro
voice_workers_disabled: true
voice_tools_enabled: false
talk_hotkey_enabled: false
ollama_host: http://127.0.0.1:1
''')
        shim = root / 'bin/codex'
        shim.parent.mkdir()
        shim.write_text('''#!/usr/bin/env python3
import json, sys
for line in sys.stdin:
    request = json.loads(line)
    if 'id' not in request:
        continue
    if request['method'] == 'model/list':
        result = {'data': [{'model': 'fixture-model'}], 'nextCursor': None}
    else:
        result = {}
    print(json.dumps({'id': request['id'], 'result': result}), flush=True)
''')
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
            if 'codex_reasoning_effort: low' not in saved:
                raise SystemExit('Codex reasoning effort did not persist as low')


if __name__ == '__main__':
    main()
