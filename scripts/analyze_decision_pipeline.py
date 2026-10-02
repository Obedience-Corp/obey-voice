#!/usr/bin/env python3
"""Summarize every retained paired observation; never drop failed runs silently."""
import collections
import hashlib
import json
import math
import statistics
import sys
from pathlib import Path

raw_path, cases_path = map(Path, sys.argv[1:3])
rows = [json.loads(line) for line in raw_path.read_text().splitlines()]
cases = {x['id']: x for x in json.loads(cases_path.read_text())}
samples = [r for r in rows if r.get('rep', -1) >= 0]
pairs = collections.defaultdict(dict)
for r in samples:
    key = (r['id'], r['rep'])
    if r['mode'] in pairs[key]:
        raise ValueError(f'duplicate sample: {key} {r["mode"]}')
    pairs[key][r['mode']] = r
if any(set(p) != {'baseline', 'optional'} for p in pairs.values()):
    raise ValueError('incomplete pair; keep raw results and finish collection before summarizing')

def stats(values):
    values = sorted(v for v in values if v is not None)
    if not values:
        return {'n': 0}
    return {'n':len(values), 'mean':statistics.mean(values), 'median':statistics.median(values),
            'p95':values[max(0, math.ceil(.95*len(values))-1)], 'min':values[0], 'max':values[-1]}

def disposition(r):
    return r['decision'].get('disposition', r['decision'].get('Disposition', ''))

def summarize(group):
    result = {'pairs':len(group), 'distinct_cases':len({p['optional']['id'] for p in group})}
    for mode in ['baseline', 'optional']:
        mode_rows = [p[mode] for p in group]
        result[mode] = {
            'correct':sum(r['correct'] for r in mode_rows),
            'errors':sum(bool(r.get('error')) for r in mode_rows),
            'missing_pcm':sum(r['first_pcm_ms'] is None for r in mode_rows),
            'worker_markers':sum(r['worker_marker_written'] for r in mode_rows),
            'model_calls':sum(r['model_calls'] for r in mode_rows),
            'admission_calls':sum(r['admission_calls'] for r in mode_rows),
            **{name:stats(r[name] for r in mode_rows) for name in ['admission_ms','first_pcm_ms','worker_start_ms','elapsed_ms']},
        }
    result['optional_minus_baseline_ms'] = {
        name:stats(p['optional'][name]-p['baseline'][name] for p in group
                   if p['optional'][name] is not None and p['baseline'][name] is not None)
        for name in ['admission_ms','first_pcm_ms','worker_start_ms','elapsed_ms']}
    return result

all_pairs = list(pairs.values())
proposed = [p for p in all_pairs if disposition(p['optional']) == 'proposed']
report = {
    'raw_sha256':hashlib.sha256(raw_path.read_bytes()).hexdigest(),
    'case_sha256':hashlib.sha256(cases_path.read_bytes()).hexdigest(),
    'manifest':rows[0],
    'note':'Timing is final transcript to software PCM / controlled worker entry, not hardware audibility or full task execution. Repetitions are not independent cases. Runtime remains resident for both modes; baseline disables decision requests.',
    'all':summarize(all_pairs),
    'proposed':summarize(proposed),
    'not_proposed':summarize([p for p in all_pairs if disposition(p['optional']) != 'proposed']),
    'conversation':summarize([p for p in all_pairs if not cases[p['optional']['id']]['want']['work']]),
    'correct_both':summarize([p for p in all_pairs if p['baseline']['correct'] and p['optional']['correct']]),
    'dispositions':dict(collections.Counter(disposition(p['optional']) for p in all_pairs)),
    'wrong_proposals':[{'id':p['optional']['id'],'rep':p['optional']['rep'],'route':p['optional']['route']} for p in proposed if not p['optional']['correct']],
    'errors':[{'id':r['id'],'rep':r['rep'],'mode':r['mode'],'error':r['error']} for r in samples if r.get('error')],
    'by_case':{key:summarize([p for p in all_pairs if p['optional']['id']==key]) for key in sorted({r['id'] for r in samples})},
}
print(json.dumps(report, indent=2))
