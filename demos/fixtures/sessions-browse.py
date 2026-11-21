#!/usr/bin/env python3
"""Write a disposable install with saved conversations for the sessions demo."""
import json
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

SESSIONS = [
    ("20260101-090000-a1", timedelta(minutes=12), [
        ("user", "What should I cook with leeks and potatoes?"),
        ("assistant", "A leek and potato soup is the easy answer. Sweat the leeks in butter, "
                      "add diced potatoes and stock, simmer twenty minutes, then blend half of it."),
        ("user", "Can I make it without cream?"),
        ("assistant", "Yes. Blending part of the potatoes gives it body on its own. "
                      "A spoon of olive oil at the end does the rest."),
        ("user", "How long does it keep?"),
        ("assistant", "Four days in the fridge. It thickens overnight, so loosen it with a "
                      "splash of water when you reheat it."),
    ]),
    ("20260101-080000-b2", timedelta(hours=5), [
        ("user", "Help me plan a weekend in Lisbon."),
        ("assistant", "Start in Alfama on Saturday morning, take tram 28 up the hill, "
                      "and leave Belém for Sunday when the queues are shorter."),
    ]),
    ("20260101-070000-c3", timedelta(days=1), [
        ("user", "Explain how a heat pump works."),
        ("assistant", "It moves heat instead of making it: a refrigerant absorbs heat outside, "
                      "a compressor raises its temperature, and it releases that heat indoors."),
    ]),
    ("20260101-060000-d4", timedelta(days=3), [
        ("user", "Remind me what we decided about the garden beds."),
        ("assistant", "Two raised beds along the south fence, cedar boards, drip line on a timer."),
    ]),
    ("20260101-050000-e5", timedelta(days=9), [
        ("user", "Old test conversation."),
        ("assistant", "Nothing here worth keeping."),
    ]),
]

CONFIG = """brain_provider: ollama
ollama_model: qwen3.8:latest
tts_provider: kokoro
models_dir: /tmp/obey-voice-demo-models
agent_name: Samantha
active_persona: samantha
"""


def main() -> None:
    root = Path(sys.argv[1])
    sessions = root / "sessions"
    sessions.mkdir(parents=True, exist_ok=True)
    (root / "config.yaml").write_text(CONFIG)
    now = datetime.now(timezone.utc)
    for session_id, age, turns in SESSIONS:
        stamp = (now - age).isoformat().replace("+00:00", "Z")
        body = {
            "format_version": 2,
            "id": session_id,
            "created_at": stamp,
            "updated_at": stamp,
            "provider": "ollama",
            "model": "qwen3.8:latest",
            "summary": turns[0][1],
            "turns": [{"role": role, "content": text} for role, text in turns],
        }
        (sessions / f"{session_id}.json").write_text(json.dumps(body, indent=2))


if __name__ == "__main__":
    main()
