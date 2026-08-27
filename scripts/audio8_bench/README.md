# Audio8-TTS ONNX benchmark harness

The measurement harness behind `docs/benchmarks/A1-audio8-tts-onnx-2026-08-27.json`.
Custom driver: the vendor's published `onnx_runtime/` code cannot run the published
INT8 export (it expects a transformer slow model with per-layer `cache_key_N` inputs;
the published slow graph is a mamba/SSM hybrid taking
`cache_keys/cache_values/conv_states/ssm_states`, with
`slow_logits_layout=relative_semantic_then_eos`). Only the vendor's tokenizer/prompt
helpers (`arktts_runtime.prompt`, `arktts_runtime.runtime._sample`) are reused.

## Setup

```sh
# 1. Vendor repo (Apache-2.0) — tokenizer/prompt helpers only, pinned to the
#    commit the artifact was measured against.
git clone https://github.com/Audio8-AI/Audio8_TTS.git
git -C Audio8_TTS checkout c856e38

# 2. Model snapshot, pinned to the revision recorded in the artifact.
hf download Audio8/audio8-TTS-0.1B-ONNX-INT8 --revision e1c07e8a3725077e3ab80ad8578e5787e8a23c6c

# 3. Python 3.12 venv with the exact measured versions.
python3.12 -m venv .venv && .venv/bin/pip install -r requirements.txt

export AUDIO8_RUNTIME_DIR=<Audio8_TTS checkout>/onnx_runtime
export AUDIO8_MODEL_DIR=<hf cache>/models--Audio8--audio8-TTS-0.1B-ONNX-INT8/snapshots/e1c07e8a3725077e3ab80ad8578e5787e8a23c6c
```

## Scripts → artifact sections

All medians land in the JSON/stdout the scripts write to the working directory;
WAVs for ear-checks land beside them.

| Command | Artifact section |
|---|---|
| `python run_bench.py 5 10` | `results.best_config_ar5_codec10` (as-shipped path) |
| `python run_bench.py 5` / `8` / `12` | `results.thread_sweep_uniform` |
| `python prefix_test.py 5` | `results.prefix_cache_experiment` |
| `AR_T=5 CODEC_T=10 CHUNK=12 python pipelined.py` | `results.cached_and_pipelined_MEASURED` |
| `python codec_scale.py` | `results.codec_scaling` |

`driver.py` is the shared engine: single-token slow-graph prefill, DualAR frame
loop, and the `relative_semantic_then_eos` semantic sampling the vendor runtime
lacks. The N7 protocol applies throughout: same two phrases as
`TestKokoroRealtimeFactor`, warm-up first, then median of 3 (RTF =
audio_seconds / synth_seconds, higher is faster).

The `baseline_same_session` kokoro numbers come from the in-repo integration
test, not from these scripts:

```sh
go test -tags integration ./pkg/voiceagent/tts/ -run TestKokoroRealtimeFactor
```
