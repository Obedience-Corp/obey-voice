#!/usr/bin/env python3
"""File-only translation/dubbing transport proof, not linguistic qualification.

Run under the existing loopback-only sandbox. Requires an installed pack and a
fresh output root. The fixture review flag explicitly approves model text only
for this transport experiment; it is not a product/user approval mechanism.
"""
import argparse
import hashlib
import json
import pathlib
import subprocess
import time
import wave


def sha(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--worker", type=pathlib.Path, required=True)
    parser.add_argument("--config", type=pathlib.Path, required=True)
    parser.add_argument("--request", type=pathlib.Path, required=True)
    parser.add_argument("--out", type=pathlib.Path, required=True)
    parser.add_argument("--fixture-review-only", action="store_true", required=True)
    args = parser.parse_args()
    worker = args.worker.resolve(strict=True)
    root = args.out.resolve()
    root.mkdir(parents=True, exist_ok=False)
    config = json.loads(args.config.read_text())
    config["storage_root"] = str(root)
    config_path = root / "config.json"
    config_path.write_text(json.dumps(config, indent=2) + "\n")
    source = json.loads(args.request.read_text())
    if source["language"] != "en" or source["target_language"] != "es":
        raise ValueError("This pinned proof requires English to Spanish")
    source["input"]["path"] = str(pathlib.Path(source["input"]["path"]).resolve(strict=True))
    source["runtime"] = config["runtime"]
    source["limits"]["timeout_seconds"] = 180
    receipts = []

    def run(request, name):
        request = dict(request, job_id=name, output_dir=str(root / name))
        request_path = root / (name + "-request.json")
        request_path.write_text(json.dumps(request, ensure_ascii=False, indent=2) + "\n")
        start = time.monotonic()
        with (root / (name + "-events.jsonl")).open("w") as events, (root / (name + "-stderr.log")).open("w") as errors:
            process = subprocess.run([str(worker), "transcribe", "audio-job", "--audio-config", str(config_path), "--request", str(request_path), "--jsonl"], stdout=events, stderr=errors, timeout=200, check=True)
        elapsed = time.monotonic() - start
        events = [json.loads(line) for line in (root / (name + "-events.jsonl")).read_text().splitlines()]
        assert [event["type"] for event in events] == ["accepted", "progress", "artifact", "completed"]
        assert all(event["seq"] == i and event["job_id"] == name and event["protocol"] == request["protocol"] for i, event in enumerate(events))
        folder = root / name
        manifest = json.loads((folder / "manifest.json").read_text())
        assert manifest["source"] == request["source"] and manifest["input"] == request["input"]
        assert manifest["language"] == request["language"] and manifest["target_language"] == request["target_language"]
        output = folder / manifest["output"]["path"]
        assert sha(output) == manifest["output"]["sha256"]
        if request["task"] == "dubbing":
            assert manifest["approved_script"] == request["script"]
            assert manifest["script_sha256"] == hashlib.sha256(request["script"].encode()).hexdigest()
            assert manifest["translation_review"] == request["translation_review"]
            assert manifest["translation_manifest_sha256"] == hashlib.sha256(request["translation_review"]["manifest_json"].encode()).hexdigest()
            with wave.open(str(output)) as wav:
                assert (wav.getnchannels(), wav.getsampwidth(), wav.getframerate(), wav.getnframes()) == (1, 2, 24000, manifest["output"]["sample_count"])
        receipts.append({"job_id": name, "exit_code": process.returncode, "elapsed_seconds": elapsed, "request_sha256": sha(request_path), "manifest_sha256": sha(folder / "manifest.json"), "manifest": manifest})
        return folder, manifest

    def dub(translation_request, folder, name):
        findings = (folder / "findings.json").read_text()
        rows = json.loads(findings)["windows"]
        if len(rows) != 1:
            raise ValueError("This fixture must not auto-concatenate overlapping windows")
        request = dict(translation_request, protocol="obey-audio-job/v3", task="dubbing", script=rows[0]["text"], instruction="", translation_review={"reviewed": True, "manifest_json": (folder / "manifest.json").read_text(), "findings_json": findings})
        return run(request, name)

    first, _ = run(source, "en-es-translation")
    spanish, speech = dub(source, first, "es-dubbing")
    converted = root / "spanish-input.wav"
    subprocess.run(["ffmpeg", "-v", "error", "-i", str(spanish / "audio.wav"), "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", str(converted)], check=True)
    with wave.open(str(converted)) as wav:
        count = wav.getnframes()
    reverse = dict(source, language="es", target_language="en", source=dict(source["source"], media_sha256=speech["output"]["sha256"], clip_id="generated-spanish-transport-fixture", range_id="full-generated-speech"), input=dict(source["input"], path=str(converted), sha256=sha(converted), sample_count=count, source_origin_samples=0, channel_role="speech"))
    back, _ = run(reverse, "es-en-translation")
    dub(reverse, back, "en-dubbing")
    assert not list(root.glob(".audio-*")), "owned runtime or artifact stage was not cleaned"
    report = {"schema": "wi-43b072.dubbing-transport-proof/v1", "worker_sha256": sha(worker), "review": "fixture-only approval of model draft, not human linguistic qualification", "input_kind": "licensed human English recording; reverse path uses generated Spanish", "qualified": False, "device_playback": False, "jobs": receipts}
    (root / "receipt.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"receipt": str(root / "receipt.json"), "jobs": len(receipts)}))


if __name__ == "__main__":
    main()
