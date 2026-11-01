#!/usr/bin/env python3
"""Quiet offline forced-alignment diagnostic; never qualifies editor word cuts.

Use the isolated qwen-asr 0.0.6 environment and explicitly downloaded pinned
model. Supplied reference text is an oracle diagnostic, not ASR output.
"""
import argparse
parser=argparse.ArgumentParser(description=__doc__)
for name in ["model", "input", "reference", "output"]:
    parser.add_argument("--"+name, required=True)
args=parser.parse_args()
import os
os.environ["HF_HUB_OFFLINE"]="1"
os.environ["TRANSFORMERS_OFFLINE"]="1"
os.environ["TOKENIZERS_PARALLELISM"]="false"
import json,time,hashlib,importlib.util
from importlib.metadata import version
from pathlib import Path
import torch
from qwen_asr import Qwen3ForcedAligner
source=Path(args.input).resolve()
model_root=Path(args.model).resolve()
# These assets were fetched from the pinned official model revision; fail before
# inference if another pack is supplied instead of labelling it as this one.
PINNED_ASSETS = {'chat_template.json': '75a8cfca24f00de72d796fbfed6858fc9614ef3dabd8696684cc3bc03a9c58ff', 'config.json': 'd616c65d46c4b90bdc651b0a0963ea932732241140f337f9bb6b0335a9c8ef09', 'generation_config.json': '948d089b23bca1d214e768d59c4438365665f52ec6d33678f4062206b3fbbb8c', 'merges.txt': '8831e4f1a044471340f7c0a83d7bd71306a5b867e95fd870f74d0c5308a904d5', 'model.safetensors': '47831d0e82f96b20e9034dba01a075ee06436654719f6a68289e49f1b65ce0e7', 'preprocessor_config.json': '45e120a4eda2c20c5d7f2ea9354e63536bf35e27aa573fb7cdf78017b378770d', 'tokenizer_config.json': '3ab80063f8511deb9566e6ad438d17b7a6277fcffd52d92854112f19d36bd81c', 'vocab.json': 'ca10d7e9fb3ed18575dd1e277a2579c16d108e32f27439684afa0e10b1440910'}
for name, expected in PINNED_ASSETS.items():
    with (model_root/name).open("rb") as file:
        if hashlib.file_digest(file,"sha256").hexdigest()!=expected:
            raise ValueError("model asset differs from pinned revision: "+name)

reference=json.loads(Path(args.reference).read_text())
spec=importlib.util.spec_from_file_location("metrics", Path(__file__).resolve().parents[3]/"scripts/measure-word-timing.py")
metrics=importlib.util.module_from_spec(spec); spec.loader.exec_module(metrics)
torch.set_num_threads(4)
began=time.monotonic()
model=Qwen3ForcedAligner.from_pretrained(str(Path(args.model).resolve()),dtype=torch.float32,device_map="cpu",local_files_only=True,attn_implementation="eager")
loaded=time.monotonic()
text=" ".join(w["text"] for w in reference)
result=model.align(audio=str(source),text=text,language="English")[0]
finished=time.monotonic()
words=[{"text":w.text,"start_us":round(w.start_time*1e6),"end_us":round(w.end_time*1e6)} for w in result.items]
matched,edit_count=metrics.align(reference,words)
edge=[abs(r[k]-w[k]) for r,w in matched for k in ["start_us","end_us"]]
filler_edge=[abs(r[k]-w[k]) for r,w in matched if metrics.token(r['text']) in metrics.FILLERS for k in ["start_us","end_us"]]
report={"model":"Qwen/Qwen3-ForcedAligner-0.6B","revision":"c7cbfc2048c462b0d63a45797104fc9db3ad62b7","input_sha256":metrics.sha(source),"transcript_kind":"manual channel-A reference; oracle diagnostic, not recognizer output","input_text":text,"words":words,"matched_words":len(matched),"edit_count_with_one_second_match_gate":edit_count,"edge_errors":metrics.errors(edge),"filler_edge_errors":metrics.errors(filler_edge),"model_load_s":loaded-began,"alignment_s":finished-loaded,"device":"cpu","dtype":"float32","batch_qualified":False}
import wave
with wave.open(str(source), "rb") as audio:
    duration_us=round(audio.getnframes()/audio.getframerate()*1e6)
report["invalid_edges"]=[word for word in words if word["start_us"]<0 or word["end_us"]<=word["start_us"] or word["end_us"]>duration_us]
report["overlapping_edges"]=[(a,b) for a,b in zip(words,words[1:]) if b["start_us"]<a["end_us"]]
report["timing_validity"]="unqualified"
report["packages"]={name:version(name) for name in ["qwen-asr","torch","transformers"]}
report["reference_sha256"]=metrics.sha(Path(args.reference))
report["probe_sha256"]=metrics.sha(Path(__file__))
Path(args.output).write_text(json.dumps(report,indent=2)+"\n")
print(json.dumps({k:v for k,v in report.items() if k not in ['words','input_text']},indent=2),flush=True)
