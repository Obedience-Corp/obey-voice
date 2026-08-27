"""Is the ~1.1s prefill avoidable? The system prompt + reference voice are IDENTICAL every turn,
so the state after them can be cached once and reused. Measure the achievable win."""
import os, copy, json, sys, time, statistics
from pathlib import Path
import numpy as np
from driver import Audio8
from arktts_runtime.prompt import format_reference_text, clean_text

SNAP = Path(os.environ["AUDIO8_MODEL_DIR"])  # snapshot e1c07e8a3725077e3ab80ad8578e5787e8a23c6c, see README.md
THREADS = int(sys.argv[1]) if len(sys.argv) > 1 else 5
a8 = Audio8(SNAP, THREADS)
ref_codes = np.load(SNAP / a8.m["reference_codes"]).astype(np.int64)
ref_text = a8.m["reference_text"]

pb = a8.prompt
prefix_parts = ["<|im_start|>system\n",
                "convert the provided text to speech reference to the following:\n\nText:\n",
                format_reference_text(ref_text), "\n\nSpeech:\n"]
prefix_tok = [t for p in prefix_parts for t in pb.encode_text(p)]
CONST = len(prefix_tok) + ref_codes.shape[1]     # system + reference voice: identical every turn
print(f"constant head = {len(prefix_tok)} text tok + {ref_codes.shape[1]} ref frames = {CONST} tokens")

# Prime once over the constant head, then snapshot.
prompt = pb.build("It continues here.", ref_text, ref_codes)
st = a8._empty_slow()
t0 = time.perf_counter()
for p in range(CONST):
    logits, hidden = a8._slow_step(prompt[:, :, p:p+1], p, st)
prime = time.perf_counter() - t0
snapshot = {k: v.copy() for k, v in st.items()}
print(f"one-time prime over constant head: {prime*1000:.0f} ms  ({prime/CONST*1000:.2f} ms/token)")

for text in ["It continues here.", "It continues here. Then a third thought arrives."]:
    pr = pb.build(text, ref_text, ref_codes)
    n = int(pr.shape[2]); var = n - CONST
    runs = []
    for _ in range(3):
        st2 = {k: v.copy() for k, v in snapshot.items()}
        t = time.perf_counter()
        for p in range(CONST, n):
            logits, hidden = a8._slow_step(pr[:, :, p:p+1], p, st2)
        runs.append(time.perf_counter() - t)
    warm = statistics.median(runs)
    print(f"  '{text[:34]:34s}' variable={var:3d} tok -> warm prefill {warm*1000:6.1f} ms "
          f"(vs ~{n*prime/CONST*1000:.0f} ms cold) : saves ~{(n*prime/CONST - warm)*1000:.0f} ms")
