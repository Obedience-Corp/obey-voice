import os, time, statistics, numpy as np
from pathlib import Path
from driver import Audio8
SNAP = Path(os.environ["AUDIO8_MODEL_DIR"])  # snapshot e1c07e8a3725077e3ab80ad8578e5787e8a23c6c, see README.md
a8=Audio8(SNAP,5,10)
rng=np.random.default_rng(0)
print(f"{'frames':>7} {'audio_s':>8} {'codec_ms':>9} {'ms/frame':>9}")
for n in (6,12,24,32,64,89,140):
    codes=rng.integers(0,4096,size=(1,10,n),dtype=np.int64)
    a8.codec.run(None,{"codes":codes})           # warm
    ts=[]
    for _ in range(3):
        t=time.perf_counter(); out=a8.codec.run(None,{"codes":codes})[0]; ts.append(time.perf_counter()-t)
    m=statistics.median(ts); aud=np.asarray(out).size/44100
    print(f"{n:>7} {aud:>8.3f} {m*1000:>9.1f} {m*1000/n:>9.2f}")
