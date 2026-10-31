import base64,hashlib,json,pathlib,time,urllib.request,urllib.error
root=pathlib.Path('/tmp/wi-43b072-sol')
source=pathlib.Path('/Users/lancerogers/Dev/AI/My_Tools/workflow/design/obey-voice-native-audio-input/proof/artifacts/addition.wav')
audio=source.read_bytes();host='http://localhost:11434';model='lfm3-audio:2b'
def get(route):
 with urllib.request.urlopen(host+route,timeout=5) as response:return json.load(response)
version=get('/api/version');tags=get('/api/tags');digest=next(m['digest'] for m in tags['models'] if m['name']==model)
receipt={'runtime':version,'model':model,'model_digest':digest,'source_sha256':hashlib.sha256(audio).hexdigest(),'purpose':'Observe configured context/no-truncation enforcement and audio prompt accounting; not meaning or listening qualification.','cases':[]}
for mode,truncate in [('text',False),('audio',False),('audio',True)]:
 messages=[{'role':'system','content':'Answer only the final user request.'},{'role':'user','content':'token '*4096},{'role':'assistant','content':'Noted.'},{'role':'user','content':'Reply with only the number 15.' if mode=='text' else ''}]
 if mode=='audio':messages[-1]['images']=[base64.b64encode(audio).decode()]
 payload={'model':model,'stream':False,'think':False,'truncate':truncate,'shift':False,'messages':messages,'options':{'num_ctx':2048,'num_predict':1,'temperature':0}}
 row={'mode':mode,'truncate':truncate,'shift':False,'num_ctx':2048,'num_predict':1,'prior_user_bytes':len(messages[1]['content']),'audio_count':int(mode=='audio')}
 start=time.monotonic();request=urllib.request.Request(host+'/api/chat',json.dumps(payload).encode(),headers={'Content-Type':'application/json'})
 try:
  with urllib.request.urlopen(request,timeout=15) as response:
   result=json.load(response);row.update(http_status=response.status,prompt_eval_count=result.get('prompt_eval_count'),eval_count=result.get('eval_count'),done=result.get('done'),done_reason=result.get('done_reason'),reply=result.get('message',{}).get('content'))
 except urllib.error.HTTPError as error:row.update(http_status=error.code,error=error.read(4096).decode(errors='replace'))
 except Exception as error:row['error']=str(error)
 row['elapsed_seconds']=round(time.monotonic()-start,3);receipt['cases'].append(row)
 (root/'native-context-accounting.json').write_text(json.dumps(receipt,indent=2));print(json.dumps(row),flush=True)
