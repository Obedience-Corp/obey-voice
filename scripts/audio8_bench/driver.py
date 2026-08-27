"""Minimal driver for the PUBLISHED Audio8 ONNX INT8 export (mamba-hybrid slow model).
The vendor's onnx_runtime/ code targets an older transformer export and cannot run these graphs."""
from __future__ import annotations
import os, json, sys, time
from pathlib import Path
import numpy as np, onnxruntime as ort

RT = Path(os.environ["AUDIO8_RUNTIME_DIR"])  # <Audio8_TTS checkout>/onnx_runtime, see README.md
sys.path.insert(0, str(RT))
from arktts_runtime.prompt import PromptBuilder
from arktts_runtime.runtime import _sample


def _sess(path: Path, threads: int) -> ort.InferenceSession:
    o = ort.SessionOptions(); o.log_severity_level = 3
    o.intra_op_num_threads = threads; o.inter_op_num_threads = max(1, threads // 2)
    return ort.InferenceSession(str(path), sess_options=o, providers=["CPUExecutionProvider"])


class Audio8:
    def __init__(self, model_dir: Path, threads: int = 5, codec_threads: int | None = None):
        self.dir = Path(model_dir)
        self.m = json.loads((self.dir / "runtime_manifest.json").read_text())
        self.slow = _sess(self.dir / self.m["slow_decode_model"], threads)
        self.fast = _sess(self.dir / self.m["fast_model"], threads)
        self.codec = _sess(self.dir / self.m["codec_models"][self.m["default_codec_precision"]], codec_threads or threads)
        self.prompt = PromptBuilder(self.dir / "tokenizer", self.m["semantic_begin_id"], self.m["num_codebooks"])
        self.nlayer = int(self.m["num_layers"])

    def _empty_slow(self):
        s = {i.name: i.shape for i in self.slow.get_inputs()}
        z = lambda n: np.zeros([int(d) for d in s[n]], dtype=np.float32)
        return dict(cache_keys=z("cache_keys"), cache_values=z("cache_values"),
                    conv_states=z("conv_states"), ssm_states=z("ssm_states"))

    def _slow_step(self, column, pos, st):
        out = self.slow.run(None, {"codes": column.astype(np.int64),
                                   "position": np.asarray([pos], dtype=np.int64), **st})
        logits, hidden, kd, vd, ncs, nss = out
        st["cache_keys"][:, :, :, pos, :] = kd
        st["cache_values"][:, :, :, pos, :] = vd
        st["conv_states"], st["ssm_states"] = ncs, nss
        return np.asarray(logits)[0, -1], np.asarray(hidden)

    def _empty_fast(self):
        n = int(self.m["num_fast_layers"])
        shp = (1, int(self.m["fast_n_local_heads"]), int(self.m["num_codebooks"]), int(self.m["fast_head_dim"]))
        c = {}
        for i in range(n):
            c[f"cache_key_{i}"] = np.zeros(shp, np.float32); c[f"cache_value_{i}"] = np.zeros(shp, np.float32)
        return c

    def _fast_step(self, hidden, token_id, use_hidden, pos, c):
        out = self.fast.run(None, {"slow_hidden": np.asarray(hidden, np.float32),
                                   "token_id": np.asarray([[token_id]], np.int64),
                                   "use_slow_hidden": np.asarray([use_hidden], np.bool_),
                                   "input_pos": np.asarray([pos], np.int64), **c})
        for i in range(int(self.m["num_fast_layers"])):
            c[f"cache_key_{i}"][:, :, pos, :] = np.asarray(out[1 + 2 * i])[:, :, 0, :]
            c[f"cache_value_{i}"][:, :, pos, :] = np.asarray(out[2 + 2 * i])[:, :, 0, :]
        return np.asarray(out[0])[0, -1]

    def _sample_semantic(self, logits, previous, temp, top_p, top_k, rng):
        begin, stop = int(self.m["semantic_begin_id"]), int(self.m["im_end_id"])
        v = np.asarray(logits).reshape(-1)          # relative_semantic_then_eos
        idx = _sample(v, temp, top_p, top_k, rng)
        hi = _sample(v, 1.0, 0.9, top_k, rng)
        pick = lambda i: stop if i == v.size - 1 else begin + i
        n = pick(idx)
        return pick(hi) if (n != stop and n in previous) else n

    def synth(self, text, ref_text, ref_codes, max_new=1024, temp=0.7, top_p=0.9, top_k=50, seed=42):
        rng = np.random.default_rng(seed)
        prompt = self.prompt.build(text, ref_text, ref_codes)   # [1, 11, N]
        n = int(prompt.shape[2])
        st = self._empty_slow()
        t0 = time.perf_counter()
        for p in range(n):                                       # prefill: one token per call
            logits, hidden = self._slow_step(prompt[:, :, p:p + 1], p, st)
        prefill = time.perf_counter() - t0

        begin, stop = int(self.m["semantic_begin_id"]), int(self.m["im_end_id"])
        nb, cbs = int(self.m["num_codebooks"]), int(self.m["codebook_size"])
        frames, previous, first_frame_at = [], [], None
        t1 = time.perf_counter()
        for step in range(max_new):
            sem = self._sample_semantic(logits, previous, temp, top_p, top_k, rng)
            if sem == stop:
                break
            previous.append(sem); previous = previous[-10:]
            fc = self._empty_fast()
            self._fast_step(hidden, 0, True, 0, fc)
            tok = min(max(sem - begin, 0), cbs - 1)
            cb = [tok]
            for fp in range(1, nb):
                tok = _sample(self._fast_step(hidden, tok, False, fp, fc), temp, top_p, top_k, rng)
                cb.append(tok)
            frames.append(np.asarray(cb, np.int64))
            if first_frame_at is None:
                first_frame_at = time.perf_counter() - t1
            col = np.concatenate([[sem], frames[-1]]).reshape(1, -1, 1)
            logits, hidden = self._slow_step(col, n + step, st)
        decode = time.perf_counter() - t1
        if not frames:
            raise RuntimeError("no frames produced")
        codes = np.stack(frames, axis=1)[np.newaxis]
        t2 = time.perf_counter()
        audio = np.asarray(self.codec.run(None, {"codes": codes})[0], np.float32).reshape(-1)
        codec_t = time.perf_counter() - t2
        return audio, dict(prompt_tokens=n, frames=len(frames), prefill_s=prefill,
                           decode_s=decode, codec_s=codec_t, first_frame_s=first_frame_at,
                           total_s=prefill + decode + codec_t)
