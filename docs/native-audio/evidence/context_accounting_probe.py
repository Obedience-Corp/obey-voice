import argparse, base64, hashlib, json, pathlib, time, urllib.request, urllib.error

parser = argparse.ArgumentParser(
    description="Quiet local context-accounting probe; no devices or model install."
)
parser.add_argument(
    "--source",
    type=pathlib.Path,
    default=pathlib.Path(__file__).resolve().parents[3]
    / "pkg/voiceagent/nativeaudio/assets/setup.wav",
)
parser.add_argument("--output", type=pathlib.Path, required=True)
args = parser.parse_args()
if not args.output.is_absolute():
    parser.error("--output must be an absolute caller-owned path")
if args.output.exists():
    parser.error("--output already exists; preserve the previous receipt")
args.output.parent.mkdir(parents=True, exist_ok=True)
source = args.source
if source.stat().st_size > 1 << 20:
    parser.error("source exceeds the persona fixture byte limit")
audio = source.read_bytes()
host = "http://localhost:11434"
model = "lfm3-audio:2b"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())


def get(route):
    with opener.open(host + route, timeout=5) as response:
        return json.load(response)


version = get("/api/version")
tags = get("/api/tags")
digest = next(m["digest"] for m in tags["models"] if m["name"] == model)
receipt = {
    "runtime": version,
    "model": model,
    "model_digest": digest,
    "source_sha256": hashlib.sha256(audio).hexdigest(),
    "purpose": "Observe configured context/no-truncation enforcement and audio prompt accounting; not meaning or listening qualification.",
    "cases": [],
}
for mode, truncate in [("text", False), ("audio", False), ("audio", True)]:
    messages = [
        {"role": "system", "content": "Answer only the final user request."},
        {"role": "user", "content": "token " * 4096},
        {"role": "assistant", "content": "Noted."},
        {
            "role": "user",
            "content": "Reply with only the number 15." if mode == "text" else "",
        },
    ]
    if mode == "audio":
        messages[-1]["images"] = [base64.b64encode(audio).decode()]
    payload = {
        "model": model,
        "stream": False,
        "think": False,
        "truncate": truncate,
        "shift": False,
        "messages": messages,
        "options": {"num_ctx": 2048, "num_predict": 1, "temperature": 0},
    }
    row = {
        "mode": mode,
        "truncate": truncate,
        "shift": False,
        "num_ctx": 2048,
        "num_predict": 1,
        "prior_user_bytes": len(messages[1]["content"]),
        "audio_count": int(mode == "audio"),
    }
    start = time.monotonic()
    request = urllib.request.Request(
        host + "/api/chat",
        json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        with opener.open(request, timeout=15) as response:
            result = json.load(response)
            row.update(
                http_status=response.status,
                prompt_eval_count=result.get("prompt_eval_count"),
                eval_count=result.get("eval_count"),
                done=result.get("done"),
                done_reason=result.get("done_reason"),
                reply=result.get("message", {}).get("content"),
            )
    except urllib.error.HTTPError as error:
        row.update(
            http_status=error.code, error=error.read(4096).decode(errors="replace")
        )
    except Exception as error:
        row["error"] = str(error)
    row["elapsed_seconds"] = round(time.monotonic() - start, 3)
    receipt["cases"].append(row)
    args.output.write_text(json.dumps(receipt, indent=2))
    print(json.dumps(row), flush=True)
