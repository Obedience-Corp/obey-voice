#!/usr/bin/env python3
"""Real TUI Settings → Tools picture-maker rows, in a throwaway HOME.

Ollama points at a closed port and nothing is generated; the check is that
the rows save image_generation and comfyui_url to the disposable config.
"""
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    binary = Path('bin/obey-voice').resolve()
    with tempfile.TemporaryDirectory(prefix='obey-picture-settings-') as temporary:
        settings = Path(temporary) / '.obey/agents/voice/obey-voice/config.yaml'
        settings.parent.mkdir(parents=True)
        settings.write_text('''brain_provider: ollama
ollama_model: fixture-model
agent_name: Obey
tts_provider: kokoro
voice_workers_disabled: true
talk_hotkey_enabled: false
ollama_host: http://127.0.0.1:1
''')
        environment = {
            'HOME': temporary, 'PATH': os.environ['PATH'],
            'TERM': 'xterm-256color', 'COLORTERM': 'truecolor',
            'CLICOLOR_FORCE': '1', 'FORCE_COLOR': '1',
            'OBEY_VOICE_COLOR_PROFILE': 'ansi',
        }
        try:
            subprocess.run([str(binary), '-t', '-n'], cwd=temporary, env=environment, check=True)
        except KeyboardInterrupt:
            pass
        finally:
            saved = settings.read_text()
            Path('/tmp/obey-picture-settings.yaml').write_text(saved)
            for line in ('image_generation: comfyui', 'comfyui_url: http://studio.local:8188'):
                if line not in saved:
                    raise SystemExit(f'{line!r} did not persist:\n{saved}')
            print('saved: image_generation comfyui, comfyui_url http://studio.local:8188')


if __name__ == '__main__':
    main()
