"""End-to-end: prefix cache + codec pipelined with the AR loop. Measures, does not project."""
import os, json, queue, statistics, sys, threading, time
from pathlib import Path
import numpy as np, soundfile as sf
from driver import Audio8, _sample
from arktts_runtime.prompt import format_reference_text

SNAP = Path(os.environ["AUDIO8_MODEL_DIR"])  # snapshot e1c07e8a3725077e3ab80ad8578e5787e8a23c6c, see README.md
CASES = [("one sentence", "It continues here."),
         ("two sentences", "It continues here. Then a third thought arrives.")]
a8 = Audio8(SNAP, int(os.environ.get("AR_T",5)), int(os.environ.get("CODEC_T",10)))
m = a8.m
hop = int(m["codec_hop_length"]); sr = int(m["sample_rate"])
ref = np.load(SNAP / m["reference_codes"]).astype(np.int64); ref_text = m["reference_text"]

pb = a8.prompt
_pre = ["<|im_start|>system\n", "convert the provided text to speech reference to the following:\n\nText:\n",
        format_reference_text(ref_text), "\n\nSpeech:\n"]
CONST = len([t for p in _pre for t in pb.encode_text(p)]) + ref.shape[1]

# --- one-time prime over the constant head (system prompt + reference voice) ---
_p = pb.build("x", ref_text, ref)
st = a8._empty_slow()
t0 = time.perf_counter()
for i in range(CONST):
    logits, hidden = a8._slow_step(_p[:, :, i:i+1], i, st)
PRIME = time.perf_counter() - t0
SNAPSHOT = {k: v.copy() for k, v in st.items()}
print(f"one-time prime: {PRIME*1000:.0f} ms over {CONST} tokens (paid once per process, not per turn)\n")


def run(text, chunk_frames, context, seed=42):
    pr = pb.build(text, ref_text, ref); n = int(pr.shape[2])
    st = {k: v.copy() for k, v in SNAPSHOT.items()}
    t_start = time.perf_counter()
    for i in range(CONST, n):
        logits, hidden = a8._slow_step(pr[:, :, i:i+1], i, st)
    t_prefill = time.perf_counter() - t_start

    q: queue.Queue = queue.Queue()
    out, ttfa, codec_busy = [], [None], [0.0]

    def worker():
        emitted = 0
        while True:
            item = q.get()
            if item is None:
                break
            frames, final = item
            start_f = max(0, len(frames) - context - chunk_frames) if context is not None else 0
            window = np.stack(frames[start_f:], axis=1)[np.newaxis]
            tc = time.perf_counter()
            audio = np.asarray(a8.codec.run(None, {"codes": window})[0], np.float32).reshape(-1)
            codec_busy[0] += time.perf_counter() - tc
            abs_start = start_f * hop
            begin = max(0, emitted - abs_start)
            if begin < audio.size:
                out.append(np.ascontiguousarray(audio[begin:])); emitted += audio.size - begin
                if ttfa[0] is None:
                    ttfa[0] = time.perf_counter() - t_start

    th = threading.Thread(target=worker, daemon=True); th.start()

    begin_id, stop_id = int(m["semantic_begin_id"]), int(m["im_end_id"])
    nb, cbs = int(m["num_codebooks"]), int(m["codebook_size"])
    rng = np.random.default_rng(seed); frames, prev = [], []
    t_ar = time.perf_counter()
    for step in range(1024):
        sem = a8._sample_semantic(logits, prev, 0.7, 0.9, 50, rng)
        if sem == stop_id: break
        prev.append(sem); prev = prev[-10:]
        fc = a8._empty_fast(); a8._fast_step(hidden, 0, True, 0, fc)
        tok = min(max(sem - begin_id, 0), cbs - 1); cb = [tok]
        for fp in range(1, nb):
            tok = _sample(a8._fast_step(hidden, tok, False, fp, fc), 0.7, 0.9, 50, rng); cb.append(tok)
        frames.append(np.asarray(cb, np.int64))
        if len(frames) % chunk_frames == 0:
            q.put((list(frames), False))
        col = np.concatenate([[sem], frames[-1]]).reshape(1, -1, 1)
        logits, hidden = a8._slow_step(col, n + step, st)
    ar_time = time.perf_counter() - t_ar
    q.put((list(frames), True)); q.put(None); th.join()
    total = time.perf_counter() - t_start
    audio = np.concatenate(out) if out else np.zeros(0, np.float32)
    return dict(prefill_s=t_prefill, ar_s=ar_time, codec_busy_s=codec_busy[0], total_s=total,
                ttfa_s=ttfa[0], audio_s=audio.size / sr, frames=len(frames),
                rtf=(audio.size / sr) / total), audio


CHUNK=int(os.environ.get("CHUNK",12))
print(f"AR_T={os.environ.get('AR_T',5)} CODEC_T={os.environ.get('CODEC_T',10)} CHUNK={CHUNK}")
print(f"{'case':14s} {'ctx':>4} {'prefill':>8} {'AR':>7} {'codec':>7} {'TOTAL':>8} {'audio':>7} {'RTF':>6} {'TTFA':>7}")
best = {}
for name, text in CASES:
    for ctx in (0,):
        rows = [run(text, CHUNK, ctx, seed=42 + i)[0] for i in range(3)]
        med = {k: statistics.median(r[k] for r in rows) for k in rows[0]}
        flag = "  <- codec cannot keep up" if med["codec_busy_s"] > med["ar_s"] else ""
        print(f"{name:14s} {ctx:>4} {med['prefill_s']*1000:>7.0f}m {med['ar_s']*1000:>6.0f}m "
              f"{med['codec_busy_s']*1000:>6.0f}m {med['total_s']*1000:>7.0f}m {med['audio_s']:>6.3f}s "
              f"{med['rtf']:>5.2f}x {med['ttfa_s']*1000:>6.0f}m{flag}")
        best[f"{name}|ctx{ctx}"] = med

json.dump(dict(prime_ms=PRIME*1000, const_head_tokens=CONST, results=best),
          open(f"audio8-pipelined-{os.environ.get('AR_T',5)}-{os.environ.get('CODEC_T',10)}-c{CHUNK}.json", "w"), indent=2)
