#!/usr/bin/env python3
"""Exercise the real engine router against a disposable camp importer."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(prefix='obey-routing-') as name:
    fixture = Path(name)
    config = fixture / 'config'
    config.mkdir()
    (config / 'config.yaml').write_text('meeting:\n  route:\n    mode: off\n')
    campaign = fixture / 'campaign'
    campaign.mkdir()
    tools = fixture / 'bin'
    tools.mkdir()
    camp = tools / 'camp'
    camp.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
root = pathlib.Path(os.environ['ROUTE_FIXTURE'])
if sys.argv[1:3] == ['list', '--json']:
 print(json.dumps([{'name':'fixture', 'path':str(root / 'campaign')}]))
elif '--help' in sys.argv:
 print('import-meeting')
elif sys.argv[1:4] == ['idea', 'notes', 'import-meeting']:
 if os.environ.get('ROUTE_FAIL'):
  print('fixture importer refused delivery', file=sys.stderr)
  sys.exit(1)
 assert pathlib.Path.cwd().resolve() == (root / 'campaign').resolve()
 summary = pathlib.Path(sys.argv[sys.argv.index('--summary-file')+1]).read_text()
 target = pathlib.Path('notes/meetings/check.md')
 target.parent.mkdir(parents=True, exist_ok=True)
 target.write_text(summary)
 print(json.dumps({'path':str(target)}))
else:
 sys.exit(2)
''')
    camp.chmod(0o755)
    engine = fixture / 'engine'
    subprocess.run(['go', 'build', '-tags', 'desktopfixture', '-o', str(engine), '.'], cwd=root / 'bridge', check=True)
    bundle = fixture / 'check.meeting'
    bundle.mkdir()
    rows = [
        {'type':'session_start', 'ts':'2026-10-20T12:00:00Z', 'description':'Routing acceptance', 'stt':'fixture'},
        {'type':'note', 'ts':'2026-10-20T12:00:01Z', 'offset_ms':1000, 'text':'Retain this action item'},
        {'type':'utterance', 'ts':'2026-10-20T12:00:02Z', 'offset_ms':2000, 'text':'Retain this transcript'},
        {'type':'session_end', 'ts':'2026-10-20T12:00:03Z', 'offset_ms':3000}]
    (bundle / '.samantha').mkdir()
    (bundle / '.samantha/events.jsonl').write_text(''.join(json.dumps(r)+'\n' for r in rows))
    env = dict(os.environ, OBEY_VOICE_TEST_CONFIG=str(config), ROUTE_FIXTURE=str(fixture), PATH=str(tools)+os.pathsep+os.environ['PATH'])
    args = [str(engine), 'meeting', 'route', str(bundle), '--to', 'camp:fixture', '--body', 'full', '--json']
    success = subprocess.run(args, env=env, text=True, capture_output=True)
    assert success.returncode == 0, (success.stdout, success.stderr)
    receipt = json.loads(success.stdout)
    assert receipt['outcome'] == 'routed', receipt
    saved = (campaign / 'notes/meetings/check.md').read_text()
    assert 'Retain this action item' in saved and 'Retain this transcript' in saved, saved
    failed = subprocess.run(args, env=dict(env, ROUTE_FAIL='1'), text=True, capture_output=True, check=True)
    receipt = json.loads(failed.stdout)
    assert receipt['outcome'] == 'failed' and 'refused delivery' in receipt['error'], receipt
    assert bundle.is_dir()
    print('Real engine routing: saved note/transcript, campaign cwd, and exit-0 failure receipt verified.')
