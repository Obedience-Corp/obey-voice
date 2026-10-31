#!/usr/bin/env python3
"""Compare explicit local recognizers on identical, licensed AMI PCM windows.

This measures text recognition, never word alignment or automatic-cut safety.
Supply existing assets/configs; this harness does not download or use devices.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import time
import wave

spec = importlib.util.spec_from_file_location(
    "word_timing", Path(__file__).with_name("measure-word-timing.py"))
timing = importlib.util.module_from_spec(spec)
spec.loader.exec_module(timing)


def tokens(text):
    return [normalized for word in text.split() if (normalized := timing.token(word))]


def score(reference, hypothesis):
    """Unit-cost word edit distance; no timestamp requirement for plain ASR."""
    previous = list(range(len(hypothesis) + 1))
    for i, expected in enumerate(reference, 1):
        current = [i]
        for j, actual in enumerate(hypothesis, 1):
            current.append(min(previous[j] + 1, current[j-1] + 1,
                               previous[j-1] + (expected != actual)))
        previous = current
    return {"reference_words": len(reference), "predicted_words": len(hypothesis),
            "word_edits": previous[-1],
            "word_edit_rate": previous[-1] / len(reference) if reference else None,
            "reference_fillers": sum(w in timing.FILLERS for w in reference),
            "predicted_fillers": sum(w in timing.FILLERS for w in hypothesis)}


def windows(sample_count):
    """Use the production native worker's fixed-30s-overlap-2s/v1 plan."""
    start = 0
    while start < sample_count:
        end = min(start + 30 * 16000, sample_count)
        yield start, end
        if end == sample_count:
            return
        start = end - 2 * 16000


def invoke(command, prefix, timeout):
    began = time.monotonic()
    timed_out = False
    with prefix.with_suffix(".jsonl").open("wb") as stdout, prefix.with_suffix(".stderr").open("wb") as stderr:
        process = subprocess.Popen(command, stdout=stdout, stderr=stderr, start_new_session=True)
        try:
            code = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
            code = process.returncode
    return {"exit_code": code, "timed_out": timed_out,
            "elapsed_s": round(time.monotonic()-began, 3)}


def completed(prefix, result, job_id):
    events = [json.loads(line) for line in prefix.with_suffix(".jsonl").read_text().splitlines()]
    if result["exit_code"] != 0 or result["timed_out"] or not events:
        raise ValueError("worker did not complete")
    if events[-1].get("type") != "completed" or events[-1].get("job_id") != job_id:
        raise ValueError("missing matching completed terminal event")
    return events


def native_text(directory, result, job_id, start, end, wav_hash):
    completed(directory / "native", result, job_id)
    artifact = directory / "artifact"
    manifest = json.loads((artifact / "manifest.json").read_text())
    findings_path = artifact / "findings.json"
    output = manifest["output"]
    if output["path"] != "findings.json" or output["sha256"] != timing.sha(findings_path):
        raise ValueError("findings do not match manifest")
    findings = json.loads(findings_path.read_text())
    parts = findings["windows"]
    if findings["schema"] != "obey-audio-findings/v1" or findings["task"] != "transcription" or len(parts) != 1:
        raise ValueError("unexpected findings task or window count")
    window = parts[0]
    if (window["start_sample"], window["end_sample"], window["input_window_sha256"]) != (start, end, wav_hash):
        raise ValueError("native input differs from scored PCM window")
    return window["text"], {"manifest": manifest, "usage": window.get("usage")}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("worker", "speech-config", "audio-config", "audio-dir", "annotations", "output"):
        parser.add_argument("--"+name, type=Path, required=True)
    parser.add_argument("--sandbox-profile", type=Path)
    parser.add_argument("--case", action="append", choices=[case[0] for case in timing.CASES])
    args = parser.parse_args()
    args.output = args.output.resolve()
    # An immutable run avoids mixing fresh inference with previous artifacts.
    args.output.mkdir(parents=True, exist_ok=False)
    config = json.loads(args.audio_config.read_text())
    config["storage_root"] = str(args.output)
    config_path = args.output / "audio-config.json"
    config_path.write_text(json.dumps(config, indent=2)+"\n")
    speech = json.loads(args.speech_config.read_text())
    report = {"schema": "obey-voice.recognition-comparison/v1", "worker_sha256": timing.sha(args.worker),
              "annotations_sha256": timing.sha(args.annotations),
              "license": "AMI Meeting Corpus, CC BY 4.0, https://groups.inf.ed.ac.uk/ami/corpus/",
              "speech_config": speech, "audio_config": config,
              "method": "identical PCM windows; normalized unit-cost word edit distance against channel-A annotations",
              "limits": ["Overlapping windows are correlated; do not pool into independent population estimates.",
                         "Other-speaker cross-talk counts as errors against channel A; listener adjudication pending.",
                         "Boundary words use the existing AMI reference inclusion rule; no timing score for LFM3 text.",
                         "Filler counts are counts, not matched-filler precision or recall.",
                         "No task qualification, alignment, silence detection or automatic-cut authorization."],
              "batch_qualified": False, "windows": []}
    command = [str(args.worker.resolve())]
    if args.sandbox_profile:
        command = ["sandbox-exec", "-f", str(args.sandbox_profile.resolve())] + command
    with timing.zipfile.ZipFile(args.annotations) as archive:
        for name, meeting, begin, duration, gain, split in timing.CASES:
            if args.case and name not in args.case:
                continue
            source = args.audio_dir / f"{meeting}.Headset-0.wav"
            source_hash = timing.sha(source)
            case_dir = args.output / name
            case_dir.mkdir()
            full = case_dir / "full.wav"
            subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-ss", str(begin),
                            "-i", str(source), "-t", str(duration), "-af", f"volume={gain}dB",
                            "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", str(full)], check=True)
            with wave.open(str(full), "rb") as audio:
                samples = audio.getnframes()
                pcm = audio.readframes(samples)
            for index, (offset, end) in enumerate(windows(samples)):
                directory = case_dir / str(index)
                directory.mkdir()
                wav = directory / "input.wav"
                with wave.open(str(wav), "wb") as audio:
                    audio.setparams((1, 2, 16000, 0, "NONE", "not compressed"))
                    audio.writeframes(pcm[offset*2:end*2])
                wav_hash = timing.sha(wav)
                start_sample = begin*16000 + offset
                end_sample = begin*16000 + end
                reference = timing.reference(archive, meeting, "A", start_sample/16000, (end-offset)/16000)
                (directory / "reference.json").write_text(json.dumps(reference, indent=2)+"\n")
                expected = tokens(" ".join(word["text"] for word in reference))
                job_id = f"{name}-{index}"
                row = {"case": name, "index": index, "split": split, "gain_db": gain,
                       "source_sha256": source_hash, "input_sha256": wav_hash,
                       "start_sample": start_sample, "end_sample": end_sample, "reference_tokens": expected}
                native_request = {"protocol": "obey-audio-job/v2", "job_id": job_id, "task": "transcription",
                    "source": {"media_sha256": source_hash, "clip_id": meeting, "range_id": job_id, "project_revision": "ami-diagnostic-v1"},
                    "input": {"path": str(wav), "sha256": wav_hash, "sample_count": end-offset,
                              "sample_rate_hz": 16000, "channels": 1, "channel_role": "microphone", "source_origin_samples": start_sample},
                    "runtime": config["runtime"], "script": "", "instruction": "", "language": "en",
                    "duration_policy": "preserve_picture", "output_dir": str(directory / "artifact"),
                    "limits": {"timeout_seconds": 180, "output_bytes": 16777216}}
                request_path = directory / "native.request.json"
                request_path.write_text(json.dumps(native_request)+"\n")
                native = invoke(command + ["transcribe", "audio-job", "--audio-config", str(config_path),
                                "--request", str(request_path), "--jsonl"], directory / "native", 210)
                try:
                    text, evidence = native_text(directory, native, job_id, start_sample, end_sample, wav_hash)
                    native.update(success=True, text=text, metrics=score(expected, tokens(text)), **evidence)
                except (ValueError, KeyError, OSError) as error:
                    native.update(success=False, error=str(error))
                speech_request = {"protocol": "obey-transcript/v2", "job_id": job_id,
                    "input": {"path": str(wav), "format": "wav_pcm_s16le", "sample_rate_hz": 16000,
                              "channels": 1, "timeline_origin_us": 0},
                    "language": "en", "timing": "word", "text_policy": "verbatim", "allow_model_download": False}
                request_path = directory / "parakeet.request.json"
                request_path.write_text(json.dumps(speech_request)+"\n")
                parakeet = invoke(command + ["transcribe", "--speech-config", str(args.speech_config.resolve()),
                                  "--request", str(request_path), "--jsonl"], directory / "parakeet", 210)
                try:
                    events = completed(directory / "parakeet", parakeet, job_id)
                    words = [word for event in events if event.get("type") == "segment" for word in event["words"]]
                    text = " ".join(word["text"] for word in words)
                    parakeet.update(success=True, text=text, metrics=score(expected, tokens(text)), accepted=events[0])
                except (ValueError, KeyError, OSError) as error:
                    parakeet.update(success=False, error=str(error))
                if timing.sha(wav) != wav_hash:
                    raise RuntimeError("comparison PCM changed during inference")
                row.update(native=native, parakeet=parakeet)
                report["windows"].append(row)
                (args.output / "report.json").write_text(json.dumps(report, indent=2)+"\n")
                print(job_id, "native", native["success"], "parakeet", parakeet["success"], flush=True)


if __name__ == "__main__":
    main()
