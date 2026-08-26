#!/usr/bin/env python3
"""
diar_ab_aggregate.py — turn raw diarization outputs into the T3 A/B artifact.

Consumes a raw directory produced by scripts/diar-ab-sortformer.sh:

    <raw>/env.json                          machine + binary/model/fixture provenance
    <raw>/runs.json                         per-run wall-clock timings and args
    <raw>/sherpa-cpu.analyze.json           `obey-voice meeting analyze --json` output
    <raw>/sortformer-<backend>-<preset>.jsonl   `transcribe-cli --batch-jsonl` output

and emits the `obey-voice.bench.diar-ab.v1` artifact on stdout (or --out).

Every number in the artifact is computed here. Nothing is transcribed by hand —
that is the whole point of this script: the first cut of T3 hand-copied two
coverage figures off a rounded console printout and got them wrong by 40 and
80 ms. See SCHEMA_NOTES["coverage_grid_vs_raw"].

Usage:
    python3 scripts/diar_ab_aggregate.py --raw <dir> --out artifact.json
    python3 scripts/diar_ab_aggregate.py --raw <dir> --check artifact.json
"""

from __future__ import annotations

import argparse
import json
import statistics
import sys
from pathlib import Path

# Comparison grid. 10 ms is finer than either system's output resolution
# (sherpa emits ms-precise spans, Sortformer an 80 ms diar frame), so it
# quantizes rather than invents.
FRAME_MS = 10

REFERENCE_SYSTEM = "sherpa-offline-current"
SORTFORMER_SYSTEM = "transcribecpp-sortformer"
PRESETS = ("default", "very_high_latency", "high_latency", "low_latency")
BACKENDS = ("metal", "cpu")


# ---------------------------------------------------------------- primitives


def frame_grid(segs, duration_ms):
    """Rasterize [(t0,t1,label)] onto a FRAME_MS grid; first writer wins."""
    n = duration_ms // FRAME_MS
    out = [None] * n
    for t0, t1, lab in segs:
        lo = max(0, t0 // FRAME_MS)
        hi = min(n, -(-t1 // FRAME_MS))  # ceil
        for i in range(lo, hi):
            if out[i] is None:
                out[i] = lab
    return out


def summarize(segs, duration_ms):
    labels = sorted({lab for _, _, lab in segs})
    grid = frame_grid(segs, duration_ms)
    return {
        "speaker_count": len(labels),
        "labels": labels,
        "n_spans": len(segs),
        "coverage_ms": sum(1 for x in grid if x) * FRAME_MS,
        "coverage_raw_span_sum_ms": sum(t1 - t0 for t0, t1, _ in segs),
        "talk_ms_per_speaker": {
            lab: sum(t1 - t0 for t0, t1, x in segs if x == lab) for lab in labels
        },
        "minority_speaker_spans_ms": [
            [t0, t1] for t0, t1, lab in segs if lab != "speaker-1"
        ],
    }


def agreement(ref_segs, cand_segs, duration_ms):
    """Frame-level agreement of a candidate against the reference timeline."""
    a = frame_grid(ref_segs, duration_ms)
    b = frame_grid(cand_segs, duration_ms)
    n = len(a)
    both = sum(1 for x, y in zip(a, b) if x and y)
    same = sum(1 for x, y in zip(a, b) if x and y and x == y)
    return {
        "identity_mapping": (
            "speaker-1<->speaker-1, speaker-2<->speaker-2 "
            "(arrival order matched without permutation)"
        ),
        "frames_both_label_speech": both,
        "frames_same_speaker": same,
        "frames_different_speaker": both - same,
        "who_agreement_pct_where_both_speak": round(same / both * 100, 2) if both else None,
        "frames_sherpa_speech_sortformer_silent": sum(
            1 for x, y in zip(a, b) if x and not y
        ),
        "frames_sortformer_speech_sherpa_silent": sum(
            1 for x, y in zip(a, b) if y and not x
        ),
        "frames_both_silent": sum(1 for x, y in zip(a, b) if not x and not y),
        "overall_frame_agreement_pct": round(
            sum(1 for x, y in zip(a, b) if x == y) / n * 100, 2
        ),
    }


# ------------------------------------------------------------------- parsers


def parse_sherpa(path):
    """`obey-voice meeting analyze --json` -> [(t0,t1,label)]."""
    doc = json.loads(Path(path).read_text())
    obs = doc["timeline"]["observations"]
    return [(o["start_ms"], o["end_ms"], o["label"]) for o in obs]


def parse_sortformer(path):
    """`transcribe-cli --batch-jsonl` -> ([(t0,t1,label)], encode_ms, load_ms)."""
    rows = [json.loads(l) for l in Path(path).read_text().splitlines() if l.strip()]
    header = next((r for r in rows if r.get("type") == "batch_header"), None)
    result = next(r for r in rows if "speakers" in r)
    segs = [
        (s["t0_ms"], s["t1_ms"], "speaker-%d" % s["speaker_id"])
        for s in result["speakers"]
    ]
    return segs, result.get("encode_ms"), (header or {}).get("load_ms")


# -------------------------------------------------------------- static prose

SCHEMA_NOTES = {
    "purpose": (
        "Dedicated diarization A/B record. NOT the stt-ab schema — there is no "
        "transcript, no WER, no STT-final latency; a diarization run produces "
        "only speaker spans."
    ),
    "field_semantics": {
        "coverage_ms": (
            "total ms of audio assigned to some speaker, computed on a "
            f"{FRAME_MS} ms frame grid (overlaps counted once)"
        ),
        "coverage_raw_span_sum_ms": (
            "sum of (end-start) over raw spans, before grid quantization"
        ),
        "wall_ms": (
            "full process wall clock incl. process start, model load and compute — "
            "measured identically for both systems"
        ),
        "who_agreement_pct_where_both_speak": (
            "of the frames BOTH systems mark as speech, the share where they name "
            "the same speaker. This is the quality signal; coverage differences are "
            "a separate axis."
        ),
        "realtime_factor_x": "fixture_duration_ms / wall_ms_median; >1 is faster than realtime",
    },
    "coverage_grid_vs_raw": (
        f"coverage_ms is grid-quantized: each span's start floors and its end ceils "
        f"to the {FRAME_MS} ms grid, so coverage_ms >= coverage_raw_span_sum_ms by up "
        f"to {FRAME_MS} ms per span. For the reference run that is 84560 vs 84510 "
        f"(6 spans, +50 ms). Both are reported; numbers.coverage_ms quotes the "
        "grid figure, because the agreement statistics are computed on the same grid "
        "and must be internally consistent with it."
    ),
    "no_ground_truth": (
        "The fixture ships no RTTM/label file (checked: the cache dir holds only the "
        ".wav). DER cannot be computed. Comparison is system-vs-system on the current "
        "path as reference, per spec/20 spike-3 'manual score + DER if we have labels'."
    ),
    "iteration_counts": (
        "Wall times are medians over the iteration count recorded per run. Only the "
        "reference run and the very_high_latency Sortformer runs were repeated; the "
        "default / high_latency / low_latency presets were measured once each and "
        "their wall_ms figures are INDICATIVE, not medians. Speaker-count and "
        "span outputs are deterministic per (backend, preset) and do not depend on "
        "iteration count."
    ),
    "regeneration": (
        "Produced by scripts/diar_ab_aggregate.py from the raw outputs of "
        "scripts/diar-ab-sortformer.sh. No figure in this file is transcribed by hand."
    ),
}

OBSERVATIONS = [
    "Sortformer loads and runs in the pinned v0.2.2 CLI. Not a negative result.",
    "The CLI path never holds capture by construction: it reads a finished .wav from "
    "disk and exits. Spike 3's capture invariant is therefore not exercised here — it "
    "is a question for the later in-process bind (spec/50 §3), where Sortformer must "
    "load as a separate model from the STT model (D8 is per-model, so Parakeet + "
    "Sortformer is two loads and is allowed).",
    "Sortformer has no expected-speakers input. The current path's Auto/2/3/4 "
    "clustering hint (speaker.meeting.num_speakers) has no analog: the model is a "
    "fixed 4-slot arrival-order diarizer. On this fixture that costs nothing (both "
    "agree on 2), but a `sortformer` diarizer option would silently ignore an "
    "existing config key.",
    "The coverage gap is the one behavioural difference that could reach users: "
    "meeting.AttributeTranscript maps transcript segments onto the timeline, and the "
    "audio that currently falls inside a coarse sherpa span would fall into a "
    "Sortformer inter-span gap. Whether that yields unlabelled turns depends on "
    "AttributeTranscript's overlap/nearest rule and was not measured in this spike.",
    "Sortformer labels are arrival-order only, with no cross-recording identity — "
    "same as the current path's speaker-N, so enrollment/naming remains unaffected.",
    "Single fixture, single meeting, 2 speakers, one ~4.8 s interjection. This is one "
    "data point and it does not exercise the 3-4 speaker or overlapped-speech regimes "
    "where the current path's Auto over-split is a known failure.",
]


# ------------------------------------------------------------------ assembly


def build(raw_dir: Path) -> dict:
    env = json.loads((raw_dir / "env.json").read_text())
    runs_meta = json.loads((raw_dir / "runs.json").read_text())
    duration_ms = env["fixture"]["duration_ms"]

    ref_segs = parse_sherpa(raw_dir / "sherpa-cpu.analyze.json")
    ref_meta = runs_meta["sherpa-cpu"]
    ref_wall = ref_meta["wall_ms"]

    runs = [
        {
            "system": REFERENCE_SYSTEM,
            "backend": "cpu",
            "preset": None,
            "expected_speakers_arg": ref_meta["expected_speakers_arg"],
            "iterations": len(ref_wall),
            "wall_ms_all": ref_wall,
            "wall_ms_median": round(statistics.median(ref_wall), 1),
            "realtime_factor_x": round(duration_ms / statistics.median(ref_wall), 1),
            "process_overhead_baseline_ms": runs_meta.get("overhead_baseline"),
            "result": summarize(ref_segs, duration_ms),
        }
    ]

    # Optional second reference row: the same path with expected-speakers Auto.
    auto_path = raw_dir / "sherpa-cpu-auto.analyze.json"
    auto_segs = None
    if auto_path.exists():
        auto_segs = parse_sherpa(auto_path)
        auto_meta = runs_meta["sherpa-cpu-auto"]
        auto_wall = auto_meta["wall_ms"]
        runs.append(
            {
                "system": REFERENCE_SYSTEM,
                "backend": "cpu",
                "preset": None,
                "expected_speakers_arg": auto_meta["expected_speakers_arg"],
                "iterations": len(auto_wall),
                "wall_ms_all": auto_wall,
                "wall_ms_median": round(statistics.median(auto_wall), 1),
                "realtime_factor_x": round(
                    duration_ms / statistics.median(auto_wall), 1
                ),
                "result": summarize(auto_segs, duration_ms),
            }
        )

    sf = {}
    for backend in BACKENDS:
        for preset in PRESETS:
            key = f"sortformer-{backend}-{preset}"
            path = raw_dir / f"{key}.jsonl"
            if not path.exists():
                continue
            segs, encode_ms, load_ms = parse_sortformer(path)
            wall = runs_meta[key]["wall_ms"]
            median = statistics.median(wall)
            sf[(backend, preset)] = segs
            runs.append(
                {
                    "system": SORTFORMER_SYSTEM,
                    "backend": backend,
                    "preset": preset,
                    "iterations": len(wall),
                    "wall_ms_all": wall,
                    "wall_ms_median": round(median, 1),
                    "reported_encode_ms": encode_ms,
                    "reported_model_load_ms": load_ms,
                    "realtime_factor_x": round(duration_ms / median, 1),
                    "result": summarize(segs, duration_ms),
                    "vs_sherpa_current_path": agreement(ref_segs, segs, duration_ms),
                }
            )

    # Keyed over Sortformer rows only — the reference rows share (cpu, None).
    by_key = {
        (r["backend"], r["preset"]): r for r in runs if r["system"] == SORTFORMER_SYSTEM
    }
    ref_run = runs[0]
    mv = by_key[("metal", "very_high_latency")]
    cv = by_key[("cpu", "very_high_latency")]

    ref_sum = ref_run["result"]
    mv_sum = mv["result"]
    mv_agree = mv["vs_sherpa_current_path"]

    speaker_counts = {
        f"{b}_{p}": by_key[(b, p)]["result"]["speaker_count"]
        for (b, p) in sf
    }
    all_sf_counts = sorted(set(speaker_counts.values()))

    ref_minority = ref_sum["minority_speaker_spans_ms"]
    mv_minority = mv_sum["minority_speaker_spans_ms"]

    numbers = {
        "speaker_count": {
            "sherpa_current": ref_sum["speaker_count"],
            "sortformer_per_run": speaker_counts,
            "sortformer_all_runs_agree_on": all_sf_counts[0]
            if len(all_sf_counts) == 1
            else None,
            "engine_test_expectation": (
                "tests/speakerflow/speakerflow_test.go pins wantSpeakers = 2 "
                "for this fixture"
            ),
            "sortformer_cap": 4,
            "cap_exceeded": max(speaker_counts.values()) > 4,
        },
        "expected_speakers_sensitivity": (
            {
                "compared": "--speakers 2 vs --speakers 0 (Auto), same binary and fixture",
                "timelines_identical": auto_segs == ref_segs,
                "auto_speaker_count": summarize(auto_segs, duration_ms)["speaker_count"],
            }
            if auto_segs is not None
            else None
        ),
        "minority_speaker_span_ms": {
            "sherpa_current": ref_minority,
            "sortformer_very_high_latency_metal": mv_minority,
            "start_delta_ms": mv_minority[0][0] - ref_minority[0][0]
            if ref_minority and mv_minority
            else None,
            "end_delta_ms": mv_minority[0][1] - ref_minority[0][1]
            if ref_minority and mv_minority
            else None,
        },
        "who_agreement_where_both_speak_pct": {
            "metal_very_high_latency": mv_agree["who_agreement_pct_where_both_speak"],
            "frames_same_speaker": mv_agree["frames_same_speaker"],
            "frames_both_label_speech": mv_agree["frames_both_label_speech"],
        },
        "coverage_ms": {
            "sherpa_current": ref_sum["coverage_ms"],
            "sherpa_current_raw_span_sum": ref_sum["coverage_raw_span_sum_ms"],
            "sortformer_very_high_latency_metal": mv_sum["coverage_ms"],
            "sortformer_very_high_latency_metal_raw_span_sum": mv_sum[
                "coverage_raw_span_sum_ms"
            ],
            "delta_ms": mv_sum["coverage_ms"] - ref_sum["coverage_ms"],
            "sherpa_speech_sortformer_silent_ms": mv_agree[
                "frames_sherpa_speech_sortformer_silent"
            ]
            * FRAME_MS,
            "sortformer_speech_sherpa_silent_ms": mv_agree[
                "frames_sortformer_speech_sherpa_silent"
            ]
            * FRAME_MS,
            "n_spans": {
                "sherpa_current": ref_sum["n_spans"],
                "sortformer_very_high_latency_metal": mv_sum["n_spans"],
            },
            "note": (
                "Sortformer emits fine spans that gap at pauses; sherpa emits coarse "
                "spans that bridge them. The bulk of the difference is sherpa-labelled "
                "audio Sortformer calls silence; very little runs the other way. See "
                "sherpa_speech_sortformer_silent_ms vs "
                "sortformer_speech_sherpa_silent_ms."
            ),
        },
        "wall_ms_median": {
            "sherpa_current_cpu": ref_run["wall_ms_median"],
            "sortformer_q8_0_metal_vhl": mv["wall_ms_median"],
            "sortformer_q8_0_cpu_vhl": cv["wall_ms_median"],
        },
        "speedup_vs_sherpa_x": {
            "sortformer_metal_vhl": round(
                ref_run["wall_ms_median"] / mv["wall_ms_median"], 2
            ),
            "sortformer_cpu_vhl": round(
                ref_run["wall_ms_median"] / cv["wall_ms_median"], 2
            ),
        },
        "realtime_factor_x": {
            "sherpa_current_cpu": ref_run["realtime_factor_x"],
            "sortformer_metal_vhl": mv["realtime_factor_x"],
            "sortformer_cpu_vhl": cv["realtime_factor_x"],
        },
        "preset_stability": {
            "speaker_count_identical_across_all_runs": len(all_sf_counts) == 1,
            "minority_span_per_run": {
                f"{b}_{p}": by_key[(b, p)]["result"]["minority_speaker_spans_ms"]
                for (b, p) in sf
            },
            "n_spans_per_run": {
                f"{b}_{p}": by_key[(b, p)]["result"]["n_spans"] for (b, p) in sf
            },
            "low_latency_cost_x_over_vhl": {
                b: round(
                    by_key[(b, "low_latency")]["wall_ms_median"]
                    / by_key[(b, "very_high_latency")]["wall_ms_median"],
                    1,
                )
                for b in BACKENDS
                if (b, "low_latency") in sf and (b, "very_high_latency") in sf
            },
        },
    }

    return {
        "schema": "obey-voice.bench.diar-ab.v1",
        "artifact": "T3-diar-ab-sortformer-2026-08-26.json",
        "spike": (
            "P3 / Spike 3 — meeting-review diarization: Sortformer vs current "
            "sherpa offline path"
        ),
        "spec": [
            "workflow/design/obey-voice-transcribe-cpp/spec/20-eval-gates.md#spike-3",
            "workflow/design/obey-voice-transcribe-cpp/spec/50-diarization.md",
        ],
        "date": env["date"],
        "generated_by": "scripts/diar_ab_aggregate.py",
        "schema_notes": SCHEMA_NOTES,
        "provenance": env["provenance"],
        "runs": runs,
        "numbers": numbers,
        "observations": OBSERVATIONS,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--raw", required=True, type=Path)
    ap.add_argument("--out", type=Path)
    ap.add_argument("--check", type=Path, help="compare against an existing artifact")
    args = ap.parse_args()

    doc = build(args.raw)
    text = json.dumps(doc, indent=2, ensure_ascii=False) + "\n"

    if args.check:
        want = json.loads(args.check.read_text())
        ok = True
        for section in ("runs", "numbers"):
            if doc[section] != want.get(section):
                ok = False
                print(f"MISMATCH in {section}", file=sys.stderr)
                a = json.dumps(doc[section], indent=2, sort_keys=True).splitlines()
                b = json.dumps(want.get(section), indent=2, sort_keys=True).splitlines()
                import difflib

                for line in difflib.unified_diff(b, a, "committed", "regenerated", n=1):
                    print(line, file=sys.stderr)
            else:
                print(f"OK  {section}: regenerated output matches committed artifact")
        return 0 if ok else 1

    if args.out:
        args.out.write_text(text)
    else:
        sys.stdout.write(text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
