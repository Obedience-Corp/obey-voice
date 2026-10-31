import base64
import io
import json
import pathlib
import time
import urllib.request
import urllib.error
import wave
import argparse

parser = argparse.ArgumentParser(
    description="Pinned local LFM3 token geometry and hard context rejection; no device or configuration changes"
)
parser.add_argument("receipt", type=pathlib.Path)
args = parser.parse_args()
out = args.receipt
if not out.is_absolute():
    parser.error("receipt must be absolute")
# Exclusive creation prevents overwriting evidence from an earlier trial.
with out.open("x"):
    pass
host = "http://localhost:11434"
model = "lfm3-audio:2b"


def call(route, payload=None, timeout=15):
    request = urllib.request.Request(
        host + route,
        data=None if payload is None else json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=timeout) as r:
        return json.load(r)


v = call("/api/version")
digest = next(m["digest"] for m in call("/api/tags")["models"] if m["name"] == model)
assert (
    v["version"] == "0.40.12"
    and digest == "19ff9eb6e621de5643e27d7a3d64e3fe62f9cd22d1471c9a34c01fb69b8e4f74"
)


def audio(n):
    b = io.BytesIO()
    with wave.open(b, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(16000)
        w.writeframes(b"\x01\x00" * n)
    return base64.b64encode(b.getvalue()).decode()


def tokens(n):
    return ((1 if n < 400 else 1 + (n - 400 + 159) // 160) + 7) // 8


receipt = {
    "runtime": v,
    "model": model,
    "digest": digest,
    "purpose": "Pinned source/observed token geometry and hard input rejection, synthetic software-only; no response quality claim",
    "cases": [],
}


def run(name, messages, expected_audio=None):
    p = {
        "model": model,
        "stream": False,
        "think": False,
        "truncate": False,
        "shift": False,
        "messages": messages,
        "options": {"num_ctx": 2048, "num_predict": 1, "temperature": 0},
    }
    row = {"name": name, "audio_tokens_from_pinned_source": expected_audio}
    t = time.monotonic()
    try:
        q = call("/api/chat", p)
        row.update(
            http_status=200,
            prompt_eval_count=q.get("prompt_eval_count"),
            done=q.get("done"),
            eval_count=q.get("eval_count"),
        )
    except urllib.error.HTTPError as e:
        row.update(http_status=e.code, error=e.read(4096).decode())
    row["elapsed_seconds"] = round(time.monotonic() - t, 3)
    receipt["cases"].append(row)
    out.write_text(json.dumps(receipt, indent=2))
    print(json.dumps(row), flush=True)


base = [{"role": "system", "content": "Respond briefly to the recording."}]
for n in [1, 400, 401, 1520, 1521, 16000, 480000]:
    run(
        "samples_" + str(n),
        base + [{"role": "user", "content": "", "images": [audio(n)]}],
        tokens(n),
    )
run(
    "two_recordings",
    base
    + [
        {"role": "user", "content": "", "images": [audio(16000)]},
        {"role": "assistant", "content": "Noted."},
        {"role": "user", "content": "", "images": [audio(1521)]},
    ],
    tokens(16000) + tokens(1521),
)
run("over_model_context", [{"role": "user", "content": "token " * 140000}])
run(
    "over_model_context_with_recording",
    [{"role": "user", "content": "token " * 140000, "images": [audio(480000)]}],
    375,
)
for row in receipt["cases"][:7]:
    assert (
        row["http_status"] == 200
        and row["prompt_eval_count"] == row["audio_tokens_from_pinned_source"] + 22
    ), row
assert receipt["cases"][7]["prompt_eval_count"] == 50, receipt["cases"][7]
for row, count in zip(receipt["cases"][8:], [140012, 140387]):
    assert (
        row["http_status"] == 400
        and str(count) + " tokens" in row["error"]
        and "128000 tokens" in row["error"]
    ), row
receipt["passed"] = True
out.write_text(json.dumps(receipt, indent=2) + "\n")
