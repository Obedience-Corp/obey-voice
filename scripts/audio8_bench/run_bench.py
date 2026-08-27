import os, json, sys, time, statistics, resource
from pathlib import Path
import numpy as np, soundfile as sf
from driver import Audio8

SNAP = Path(os.environ["AUDIO8_MODEL_DIR"])  # snapshot e1c07e8a3725077e3ab80ad8578e5787e8a23c6c, see README.md
THREADS = int(sys.argv[1]) if len(sys.argv) > 1 else 5
CODEC_THREADS = int(sys.argv[2]) if len(sys.argv) > 2 else None
CASES = [("one sentence",  "It continues here."),
         ("two sentences", "It continues here. Then a third thought arrives.")]
RUNS = 3

t0 = time.perf_counter(); a8 = Audio8(SNAP, THREADS, CODEC_THREADS); cold = time.perf_counter() - t0
sr = int(a8.m["sample_rate"])
ref_codes = np.load(SNAP / a8.m["reference_codes"]).astype(np.int64)
ref_text = a8.m["reference_text"]
print(f"threads={THREADS}  cold_start={cold*1000:.0f} ms  sr={sr}  ref_frames={ref_codes.shape[1]}")

a8.synth("warm up", ref_text, ref_codes, max_new=32)   # warm

out = {}
for name, text in CASES:
    rows = []
    for i in range(RUNS):
        audio, st = a8.synth(text, ref_text, ref_codes, seed=42 + i)
        st["audio_s"] = len(audio) / sr
        st["rtf"] = st["audio_s"] / st["total_s"]
        rows.append(st)
        if i == 0:
            sf.write(f"audio8-{name.replace(' ','_')}.wav", audio, sr)
    med = lambda k: statistics.median(r[k] for r in rows)
    out[name] = {k: med(k) for k in ("prompt_tokens","frames","prefill_s","decode_s","codec_s","first_frame_s","total_s","audio_s","rtf")}
    o = out[name]
    print(f"{name:14s} prompt={int(o['prompt_tokens'])}tok frames={int(o['frames'])} "
          f"prefill={o['prefill_s']*1000:6.0f}ms decode={o['decode_s']*1000:6.0f}ms codec={o['codec_s']*1000:5.0f}ms "
          f"| total={o['total_s']*1000:7.0f}ms audio={o['audio_s']:.3f}s RTF={o['rtf']:.2f}x")
    print(f"{'':14s} TTFA(first codec frame after prefill)={o['first_frame_s']*1000:.0f}ms  "
          f"=> first-audio-possible ≈ {(o['prefill_s']+o['first_frame_s'])*1000:.0f}ms")

peak = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss/(1024*1024)
print(f"peak RSS {peak:.0f} MB")
json.dump(dict(threads=THREADS, cold_start_s=cold, sample_rate=sr, peak_rss_mb=peak, results=out),
          open(f"audio8-bench-t{THREADS}-c{CODEC_THREADS or THREADS}.json","w"), indent=2)
