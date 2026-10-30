import base64,hashlib,json,pathlib,time,urllib.request,wave
root=pathlib.Path('/tmp/wi-43b072-sol');audio=(root/'source.wav').read_bytes();model='lfm3-audio:2b'
def post(route,data):
 start=time.monotonic();req=urllib.request.Request('http://localhost:11434'+route,data=json.dumps(data).encode(),headers={'Content-Type':'application/json'})
 with urllib.request.urlopen(req,timeout=180) as r:b=r.read()
 return b,time.monotonic()-start
instruction='Repeat all spoken words from the recording exactly as heard. Preserve every word and repetition. Do not answer the speakers or summarize. Return only the words to be spoken.'
b,seconds=post('/api/chat',dict(model=model,think=False,stream=False,messages=[dict(role='system',content=instruction),dict(role='user',content='',images=[base64.b64encode(audio).decode()])],options=dict(temperature=0)))
reply=json.loads(b)['message']['content'];(root/'recorded-chat.json').write_bytes(b)
pcm,speech_seconds=post('/v1/audio/speech',dict(model=model,input=reply,voice='female',response_format='pcm'))
with wave.open(str(root/'recorded-staged-output.wav'),'wb') as w:w.setnchannels(1);w.setsampwidth(2);w.setframerate(24000);w.writeframes(pcm)
receipt=dict(transport='chat_then_speech',model=model,source_sha256=hashlib.sha256(audio).hexdigest(),input_samples=480000,input_seconds=30,source_origin_samples=2080000,prompt=instruction,chat_seconds=seconds,reply=reply,speech_seconds=speech_seconds,output_samples=len(pcm)//2,output_seconds=len(pcm)/48000,output_sha256=hashlib.sha256((root/'recorded-staged-output.wav').read_bytes()).hexdigest(),qualified_conversion=False,note='Experimental staged recording reproduction; no claim of direct conditioned speech or preserved prosody. No playback/listener test.')
(root/'recorded-probe.json').write_text(json.dumps(receipt,indent=2));print(json.dumps(receipt,indent=2))
