#!/usr/bin/env python3
"""Offline oracle/recognizer alignment diagnostic on the frozen AMI windows.

This does not install models or qualify editor cuts. Run in an isolated reference
environment under a network-denied sandbox. Invalid edges remain in the report.
"""
import argparse
import importlib.util
from importlib.metadata import version
import json
import os
from pathlib import Path
import platform
import time
import wave

os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["TRANSFORMERS_OFFLINE"] = "1"
os.environ["TOKENIZERS_PARALLELISM"] = "false"

import numpy as np
import torch

from ctc_path import forced_path

REPO = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location("metrics", REPO / "scripts/measure-word-timing.py")
metrics = importlib.util.module_from_spec(spec)
spec.loader.exec_module(metrics)

RECEIPTS = {
    "qwen": REPO / "docs/audio-jobs/evidence/alignment-smoke-2026-10-30/model-receipt.json",
    "ctc": REPO / "docs/audio-jobs/evidence/alignment-2026-10-30/ctc-model.json",
}


def check_model(provider, model):
    receipt = json.loads(RECEIPTS[provider].read_text())
    for entry in receipt["files"]:
        if entry["path"] == "README.md":
            continue
        path = model / entry["path"]
        if path.stat().st_size != entry["bytes"] or metrics.sha(path) != entry["sha256"]:
            raise ValueError("model file differs from pinned receipt: " + entry["path"])
    return receipt


def tokenize(text):
    return [word for part in text.split() if (word := metrics.token(part))]


def measurements(reference, words, supplied, duration_us):
    matched, edit_count = metrics.align(reference, words)
    edges = [abs(r[k]-w[k]) for r, w in matched for k in ("start_us", "end_us")]
    filler_edges = [abs(r[k]-w[k]) for r, w in matched
                    if metrics.token(r["text"]) in metrics.FILLERS
                    for k in ("start_us", "end_us")]
    invalid = [index for index, word in enumerate(words)
               if word["start_us"] < 0 or word["end_us"] <= word["start_us"]
               or word["end_us"] > duration_us]
    overlapping = [index for index in range(1, len(words))
                   if words[index]["start_us"] < words[index-1]["end_us"]]
    preserved = tokenize(" ".join(word["text"] for word in words)) == tokenize(supplied)
    return {"reference_words": len(reference), "returned_words": len(words),
            "matched_words_within_one_second": len(matched),
            "word_edits_with_one_second_match_gate": edit_count,
            "all_matched_edge_error": metrics.errors(edges),
            "matched_filler_edge_error": metrics.errors(filler_edges),
            "invalid_edge_indices": invalid, "overlapping_edge_indices": overlapping,
            "supplied_tokens_preserved": preserved,
            "geometry_valid": not invalid and not overlapping and preserved,
            "timing_validity": "unqualified", "batch_qualified": False}


class CTCAligner:
    def __init__(self, model):
        from transformers import Wav2Vec2ForCTC, Wav2Vec2Processor
        self.processor = Wav2Vec2Processor.from_pretrained(model, local_files_only=True)
        self.model = Wav2Vec2ForCTC.from_pretrained(
            model, local_files_only=True, use_safetensors=True, attn_implementation="eager").eval()
        self.vocabulary = self.processor.tokenizer.get_vocab()
        self.blank = self.model.config.pad_token_id
        self.stride, self.receptive = 1, 1
        for kernel, stride in zip(self.model.config.conv_kernel, self.model.config.conv_stride):
            self.receptive += (kernel-1)*self.stride
            self.stride *= stride
        self.offset = (self.receptive-self.stride)/2

    def prepare(self, waveform):
        inputs = self.processor(waveform, sampling_rate=16000, return_tensors="pt")
        with torch.inference_mode():
            logits = self.model(**inputs).logits[0]
        return torch.log_softmax(logits, dim=-1).numpy()

    def align(self, emissions, supplied):
        words = tokenize(supplied)
        targets, ranges = [], []
        for index, word in enumerate(words):
            if index:
                targets.append(self.vocabulary["|"])
            begin = len(targets)
            for character in word.upper():
                if character not in self.vocabulary:
                    raise ValueError("unsupported transcript character: " + character)
                targets.append(self.vocabulary[character])
            ranges.append((begin, len(targets)))
        spans, score = forced_path(emissions, targets, self.blank)
        result = []
        for word, (begin, end) in zip(words, ranges):
            start_frame, end_frame = spans[begin][0], spans[end-1][1]
            result.append({"text": word,
                           "start_us": round((self.offset+start_frame*self.stride)/16000*1e6),
                           "end_us": round((self.offset+end_frame*self.stride)/16000*1e6)})
        return result, {"path_log_probability": score,
                        "frame_stride_samples": self.stride,
                        "receptive_field_samples": self.receptive,
                        "frame_cell_origin_samples": self.offset}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--provider", choices=RECEIPTS, required=True)
    for name in ("model", "comparison", "output"):
        parser.add_argument("--"+name, type=Path, required=True)
    parser.add_argument("--case", action="append")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    comparison = json.loads((args.comparison / "report.json").read_text())
    receipt = check_model(args.provider, args.model.resolve())
    torch.set_num_threads(4)
    began = time.monotonic()
    if args.provider == "ctc":
        model = CTCAligner(str(args.model.resolve()))
    else:
        from qwen_asr import Qwen3ForcedAligner
        model = Qwen3ForcedAligner.from_pretrained(
            str(args.model.resolve()), dtype=torch.float32, device_map="cpu",
            local_files_only=True, attn_implementation="eager")
    report = {"schema": "obey-voice.alignment-comparison/v1", "provider": args.provider,
              "model": receipt, "packages": {name: version(name) for name in ("torch", "transformers", "qwen-asr", "numpy")},
              "comparison_sha256": metrics.sha(args.comparison / "report.json"),
              "probe_sha256": metrics.sha(Path(__file__)),
              "ctc_path_sha256": metrics.sha(Path(__file__).with_name("ctc_path.py")),
              "platform": platform.platform(), "device": "cpu", "dtype": "float32",
              "model_load_s": time.monotonic()-began,
              "timing_validity": "unqualified", "batch_qualified": False,
              "limits": ["Manual channel-A text is an oracle diagnostic, not recognizer output.",
                         "Wrong/missing ASR words remain wrong/missing after alignment.",
                         "Only matches within one second contribute edge metrics; unmatched counts remain explicit.",
                         "Overlapping windows and quiet variants are correlated, not independent population samples.",
                         "Other-speaker cross-talk is not represented by channel-A annotations.",
                         "Log probabilities are not calibrated word confidence.",
                         "Per-stage elapsed time is not a controlled cold/warm resource benchmark."],
              "windows": []}
    for window in comparison["windows"]:
        if args.case and window["case"] not in args.case:
            continue
        directory = args.comparison / window["case"] / str(window["index"])
        wav = directory / "input.wav"
        if metrics.sha(wav) != window["input_sha256"]:
            raise ValueError("comparison PCM hash changed")
        reference = json.loads((directory / "reference.json").read_text())
        with wave.open(str(wav), "rb") as audio:
            if (audio.getnchannels(), audio.getsampwidth(), audio.getframerate()) != (1, 2, 16000):
                raise ValueError("expected 16kHz mono PCM16")
            waveform = np.frombuffer(audio.readframes(audio.getnframes()), dtype="<i2").astype(np.float32)/32768
        row = {"case": window["case"], "index": window["index"],
               "input_sha256": window["input_sha256"], "reference_sha256": metrics.sha(directory / "reference.json"),
               "source_start_sample": window["start_sample"], "source_end_sample": window["end_sample"],
               "results": {}}
        emission_began = time.monotonic()
        emissions = model.prepare(waveform) if args.provider == "ctc" else None
        row["emission_s"] = time.monotonic()-emission_began if emissions is not None else None
        transcripts = {"oracle": " ".join(word["text"] for word in reference)}
        for recognizer in ("parakeet", "native"):
            candidate = window[recognizer]
            transcripts[recognizer] = candidate["text"] if candidate["success"] else ""
        for kind, supplied in transcripts.items():
            if not tokenize(supplied):
                row["results"][kind] = {"status": "no_transcript", "words": []}
                continue
            began = time.monotonic()
            try:
                if args.provider == "ctc":
                    words, metadata = model.align(emissions, supplied)
                else:
                    result = model.align(audio=(waveform, 16000), text=supplied, language="English")[0]
                    words = [{"text": word.text, "start_us": round(word.start_time*1e6),
                              "end_us": round(word.end_time*1e6)} for word in result.items]
                    metadata = {"timestamp_step_ms": float(model.timestamp_segment_time)}
                measured = measurements(reference, words, supplied, round(len(waveform)/16000*1e6))
                row["results"][kind] = {"status": "measured", "supplied_text": supplied,
                    "words": words, "metrics": measured, "metadata": metadata,
                    "elapsed_s": time.monotonic()-began}
            except (ValueError, RuntimeError, IndexError) as error:
                row["results"][kind] = {"status": "failed", "error": str(error),
                    "supplied_text": supplied, "elapsed_s": time.monotonic()-began}
        report["windows"].append(row)
        (args.output / "report.json").write_text(json.dumps(report, indent=2)+"\n")
        print(window["case"], window["index"], {key: value["status"] for key, value in row["results"].items()}, flush=True)


if __name__ == "__main__":
    main()
