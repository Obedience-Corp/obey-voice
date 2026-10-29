#!/usr/bin/env python3
"""Reproduce a small AMI diagnostic; never qualifies automatic cleanup.

Supply downloaded, CC BY 4.0 AMI assets explicitly. No network access here.
Outputs source hashes, raw JSONL, stderr, reference words and measured errors.
"""
import argparse
import hashlib
import json
import math
from pathlib import Path
import re
import statistics
import subprocess
import time
import xml.etree.ElementTree as ET
import zipfile

# Fixed excerpts chosen from annotations, before decoding the held-out speaker.
CASES = [
    ("initial-filler", "ES2002a", 130, 30, 0, "calibration"),
    ("quiet-filler", "ES2002a", 130, 30, -18, "diagnostic variant"),
    ("meaningful-like-pauses", "ES2002a", 300, 60, 0, "calibration"),
    ("later-background", "ES2002a", 600, 90, 0, "calibration"),
    ("heldout-introduction", "ES2003a", 45, 15, 0, "held-out speaker"),
    ("heldout-repeats", "ES2003a", 748, 62, 0, "held-out speaker"),
]
FILLERS = {"um", "uh", "ah", "uhm", "umm", "uhh", "ummm", "uhhh", "ahh", "ahhh"}


def token(text):
    return re.sub(r"[^\w']", "", text.casefold())


def sha(path):
    with path.open("rb") as file:
        return hashlib.file_digest(file, "sha256").hexdigest()


def reference(archive, meeting, agent, start, duration):
    root = ET.fromstring(archive.read(f"words/{meeting}.{agent}.words.xml"))
    return [dict(text=w.text, start_us=round((float(w.get("starttime")) - start) * 1e6),
                 end_us=round((float(w.get("endtime")) - start) * 1e6))
            for w in root if w.tag == "w" and w.get("punc") != "true"
            and w.text and token(w.text) and w.get("starttime") and w.get("endtime")
            and start <= float(w.get("starttime")) < start + duration
            and float(w.get("endtime")) <= start + duration
            and float(w.get("endtime")) > float(w.get("starttime"))]


def align(ref, hyp):
    """Minimum edit alignment; exact matches also require starts within 1 s.

    This prevents an omitted filler matching a later identical filler. ASR
    insertions from cross-talk still count as errors against channel A.
    """
    n, m = len(ref), len(hyp)
    dp = [[0] * (m + 1) for _ in range(n + 1)]
    for i in range(n + 1): dp[i][0] = i
    for j in range(m + 1): dp[0][j] = j
    def same(i, j):
        return (token(ref[i]["text"]) == token(hyp[j]["text"])
                and abs(ref[i]["start_us"] - hyp[j]["start_us"]) <= 1_000_000)
    for i in range(1, n + 1):
        for j in range(1, m + 1):
            dp[i][j] = min(dp[i-1][j]+1, dp[i][j-1]+1, dp[i-1][j-1]+(not same(i-1,j-1)))
    i, j, matches = n, m, []
    while i or j:
        if i and j and dp[i][j] == dp[i-1][j-1] + (not same(i-1,j-1)):
            if same(i-1,j-1): matches.append((ref[i-1], hyp[j-1]))
            i, j = i-1, j-1
        elif i and dp[i][j] == dp[i-1][j]+1: i -= 1
        else: j -= 1
    return matches, dp[n][m]


def precision_lower95(successes, trials):
    if not successes or not trials:
        return 0.0 if trials else None
    lo, hi = 0.0, 1.0
    for _ in range(80):
        p = (lo + hi) / 2
        tail = sum(math.comb(trials, k) * p**k * (1-p)**(trials-k) for k in range(successes, trials+1))
        if tail < .05: lo = p
        else: hi = p
    return (lo + hi) / 2


def errors(values):
    values = sorted(values)
    return dict(n=len(values), median_ms=statistics.median(values) / 1000,
                p95_ms=values[math.ceil(.95 * len(values))-1] / 1000) if values else None


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ["binary", "speech-config", "audio-dir", "annotations", "output"]:
        p.add_argument("--" + name, type=Path, required=True)
    args = p.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    archive = zipfile.ZipFile(args.annotations)
    report = dict(binary_sha256=sha(args.binary), annotations_sha256=sha(args.annotations),
                  license="AMI Meeting Corpus, CC BY 4.0, https://groups.inf.ed.ac.uk/ami/corpus/",
                  batch_qualified=False, fixtures=[])
    for name, meeting, start, duration, gain, split in CASES:
        source = args.audio_dir / f"{meeting}.Headset-0.wav"
        wav = args.output / f"{name}.wav"
        cmd = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-ss", str(start),
               "-i", str(source), "-t", str(duration), "-af", f"volume={gain}dB",
               "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", str(wav)]
        subprocess.run(cmd, check=True)
        ref = reference(archive, meeting, "A", start, duration)
        others = sum((reference(archive, meeting, agent, start, duration) for agent in "BCD"), [])
        overlap = sum(any(max(w['start_us'], o['start_us']) < min(w['end_us'], o['end_us']) for o in others) for w in ref)
        (args.output / f"{name}.reference.json").write_text(json.dumps(ref, indent=2) + "\n")
        request = dict(protocol="obey-transcript/v2", job_id=name,
                       input=dict(path=str(wav.resolve()), format="wav_pcm_s16le", sample_rate_hz=16000, channels=1, timeline_origin_us=0),
                       language="en", timing="word", text_policy="verbatim", allow_model_download=False)
        request_path = args.output / f"{name}.request.json"
        request_path.write_text(json.dumps(request))
        began = time.monotonic()
        result = subprocess.run([str(args.binary), "transcribe", "--speech-config", str(args.speech_config),
                                 "--request", str(request_path.resolve()), "--jsonl"], capture_output=True, timeout=600)
        elapsed = time.monotonic() - began
        (args.output / f"{name}.jsonl").write_bytes(result.stdout)
        (args.output / f"{name}.stderr").write_bytes(result.stderr)
        events = [json.loads(line) for line in result.stdout.splitlines()]
        terminal = events[-1] if events else {}
        success = result.returncode == 0 and terminal.get("type") == "completed"
        # A failed stream supplies no usable transcript, even if it emitted segments.
        hyp = [w for e in events if e.get("type") == "segment" for w in e["words"]] if success else []
        matched, edit_count = align(ref, hyp)
        expected_fillers = sum(token(w['text']) in FILLERS for w in ref)
        predicted_fillers = sum(token(w['text']) in FILLERS for w in hyp)
        true_fillers = sum(token(r['text']) in FILLERS for r, _ in matched)
        edges = [abs(r[k]-h[k]) for r,h in matched for k in ['start_us','end_us']]
        filler_edges = [abs(r[k]-h[k]) for r,h in matched if token(r['text']) in FILLERS for k in ['start_us','end_us']]
        row = dict(name=name, split=split, meeting=meeting, channel=0, agent="A", start_s=start, duration_s=duration,
                   gain_db=gain, source_sha256=sha(source), pcm_wav_sha256=sha(wav), elapsed_s=round(elapsed,3),
                   success=success, terminal=terminal, reference_words=len(ref), words_overlapping_other_speakers=overlap,
                   predicted_words=len(hyp), aligned_words=len(matched), word_edit_rate=edit_count/len(ref) if ref else None,
                   expected_fillers=expected_fillers, predicted_fillers=predicted_fillers, matched_fillers=true_fillers,
                   filler_precision=true_fillers/predicted_fillers if predicted_fillers else None,
                   filler_precision_lower95=precision_lower95(true_fillers, predicted_fillers),
                   filler_recall=true_fillers/expected_fillers if expected_fillers else None,
                   all_matched_edge_error=errors(edges), filler_matched_edge_error=errors(filler_edges),
                   meaningful_like=[dict(reference=r, hypothesis=h) for r,h in matched if token(r['text']) == 'like'])
        report['fixtures'].append(row)
        print(name, 'completed' if success else 'FAILED', f'fillers {true_fillers}/{expected_fillers}', flush=True)
        (args.output / 'report.json').write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    main()
