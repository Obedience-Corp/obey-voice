"""Validate design artifacts and model decisions; does not execute production routing."""
import argparse
import copy
import json
from pathlib import Path
from jsonschema import Draft202012Validator, ValidationError

HERE=Path(__file__).resolve().parent
SCHEMA=json.loads((HERE/'decisions.schema.json').read_text())

def validate(point, decision, context):
    Draft202012Validator(SCHEMA['$defs'][point]).validate(decision)
    kind=decision['kind']
    if point=='audience':
        members=decision['member_keys']
        known={p['key'] for p in context['participants']}
        assert set(members)<=known, 'member outside roster'
        assert (kind in ['group','direct']) == bool(members), 'audience member cardinality'
        assert (kind=='group') == (decision['coverage']!='none'), 'audience coverage'
        if kind=='group': assert len(members)>=1, 'group needs a resolved member'
        if kind=='direct': assert len(members)==1, 'direct needs one member'
        if kind=='current': assert decision['reason']=='none' and not decision['unresolved_names'], 'current metadata'
        if kind in ['direct','group']:
            assert not decision['unresolved_names'], 'unresolved explicit target'
            assert decision['reason'] in ['explicit','contextual'], 'missing invitation basis'
        if kind=='clarify': assert decision['reason'] in ['unknown','ambiguous'], 'clarify reason'
    elif point=='next':
        if kind=='yield':
            assert decision['member_key']=='' and decision['reason']=='no_contribution', 'invalid yield'
        else:
            assert decision['member_key'] in context['eligible_keys'], 'ineligible selection'
            assert decision['reason']!='no_contribution', 'selection has no reason'
    else:
        if kind=='pass':
            assert decision['reason']=='no_contribution' and not decision['intent'] and not decision['references'], 'invalid private pass'
        else:
            assert decision['reason']!='no_contribution' and decision['intent'].strip(), 'empty contribution'
            delivered={r['response_id'] for r in context.get('delivered',[])}
            assert set(decision['references'])<=delivered, 'undelivered reference'
            grounded=['answer','correction','challenge','revision']
            returning=any(r['key']==context.get('candidate',{}).get('key') for r in context.get('delivered',[]))
            if returning: assert decision['reason'] in grounded, 'returning speaker needs a grounded purpose'
            if returning or decision['reason'] in grounded:
                assert decision['references'], 'response reference required'

def fixtures():
    Draft202012Validator.check_schema(SCHEMA)
    roster=json.loads((HERE/'audience-cases.json').read_text())[0]['context']
    context=dict(roster,eligible_keys=['p0','p1'],delivered=[{'response_id':'response-b','key':'p1','text':'A question'}],candidate={'key':'p0'})
    examples=json.loads((HERE/'examples.json').read_text())
    for point,d in examples.items(): validate(point,d,context)
    invalid=[]
    d=copy.deepcopy(examples['audience']); d['member_keys']=['p7']; invalid.append(('audience',d))
    d=copy.deepcopy(examples['audience']); d['kind']='direct'; invalid.append(('audience',d))
    d=copy.deepcopy(examples['audience']); d['unresolved_names']=['Athena']; invalid.append(('audience',d))
    d=copy.deepcopy(examples['next']); d['member_key']='p7'; invalid.append(('next',d))
    d=copy.deepcopy(examples['next']); d['kind']='yield'; invalid.append(('next',d))
    d=copy.deepcopy(examples['participant']); d['references']=['not-delivered']; invalid.append(('participant',d))
    d=copy.deepcopy(examples['participant']); d['kind']='pass'; invalid.append(('participant',d))
    d=copy.deepcopy(examples['participant']); d['tools']=['shell']; invalid.append(('participant',d))
    for point,d in invalid:
        try: validate(point,d,context)
        except (AssertionError,ValidationError): continue
        raise AssertionError('accepted invalid decision '+str(d))
    # Ensure trace files describe valid witness outputs without treating them as runtime proof.
    traces=json.loads((HERE/'policy-traces.json').read_text())
    for trace in traces:
        assert set(trace['expect']['spoken'])<=set(trace['members'])
        assert set(trace['expect']['uncovered'])<=set(trace['members'])
        assert all(trace['expect']['spoken'].count(k)<=2 for k in trace['members'])
        assert all(a!=b for a,b in zip(trace['expect']['spoken'],trace['expect']['spoken'][1:]))
    host=json.loads((HERE/'host-state.schema.json').read_text())
    Draft202012Validator.check_schema(host)
    fences=json.loads((HERE/'commit-cases.json').read_text())
    for case in fences:
        Draft202012Validator(host['$defs']['ticket']).validate(case['ticket'])
        # Reference safety predicate, to be replayed against the actual host at implementation.
        accepted=(case['ticket']==case['current'] and case['now_ms']<case['ticket']['expires_at_monotonic_ms'] and not case['cancelled'] and case['budget_remaining'])
        assert accepted==case['expect'], 'fence witness '+case['id']
    return {'decision_schemas':3,'host_schemas':3,'valid_examples':len(examples),'invalid_examples_rejected':len(invalid),'trace_witnesses':len(traces),'fence_cases':len(fences)}

def probe_report(path):
    data=json.loads(path.read_text())
    output={'model':data['model'],'path':str(path),'cases':[]}
    for item in data['results']:
        if item['id'].startswith('legacy-'): continue
        errors=[]
        try: validate(item['point'],item['decision'],item['payload'])
        except Exception as error: errors.append(type(error).__name__+': '+str(error).splitlines()[0])
        if 'expect' in item:
            for key,value in item['expect'].items():
                if item.get('decision',{}).get(key)!=value: errors.append('expected '+key+'='+str(value))
        output['cases'].append({'id':item['id'],'valid':not errors,'errors':errors,'wall_ms':item['wall_ms']})
    audience=[i for i in data['results'] if 'expect' in i]
    output['audience_exact']=sum(i.get('matches',False) for i in audience)
    output['audience_total']=len(audience)
    output['accepted_decisions']=sum(c['valid'] for c in output['cases'])
    return output

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--probe',action='append',default=[])
    parser.add_argument('--report')
    args=parser.parse_args()
    result={'fixtures':fixtures(),'probes':[probe_report(Path(p)) for p in args.probe]}
    encoded=json.dumps(result,indent=2)+'\n'
    if args.report: Path(args.report).write_text(encoded)
    print(encoded)
    if any(not c['valid'] for p in result['probes'] for c in p['cases']): raise SystemExit(1)

if __name__=='__main__': main()
