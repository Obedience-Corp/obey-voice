"""Generate the parity evidence for the pure-Go BPE (pkg/voiceagent/tts/hfbpe).

Everything the Go tests compare against is produced here by the reference
implementation - the HuggingFace `tokenizers` library reading the Audio8
checkpoint's own tokenizer.json. Nothing is hand-written.

    export AUDIO8_MODEL_DIR=<hf cache>/models--Audio8--audio8-TTS-0.1B-ONNX-INT8/snapshots/<rev>
    python gen_parity.py            # writes into pkg/voiceagent/tts/hfbpe/testdata

Outputs:

  audio8_parity.json          every corpus case -> the real checkpoint's ids.
                              The Go test needs the real tokenizer.json (5.8 MB,
                              not vendored) and skips without it.
  mini_tokenizer.json         a small tokenizer trained here with the SAME
                              pre-tokenizer sequence, added-token shapes and BPE
                              settings as the checkpoint.
  mini_parity.json            the same corpus through that mini tokenizer. This
                              pair runs everywhere, so the pipeline is under
                              reference-generated test on every `go test`.
  punctuation_codepoints.json every code point the reference's Punctuation
                              pre-tokenizer splits on, swept one at a time.
                              Pins the table in punctuation.go.

Also re-derives pkg/voiceagent/tts/testdata/audio8_tokenizer_fixture.json and
fails if the checked-in fixture disagrees: the integration test's captured ids
and the parity corpus have to come from the same tokenizer.
"""

from __future__ import annotations

import hashlib
import json
import os
import sys
from pathlib import Path

from tokenizers import AddedToken, Tokenizer, models, trainers

import corpus

REPO = Path(__file__).resolve().parents[2]
OUT = REPO / "pkg" / "voiceagent" / "tts" / "hfbpe" / "testdata"
FIXTURE = REPO / "pkg" / "voiceagent" / "tts" / "testdata" / "audio8_tokenizer_fixture.json"

# Extra probes that only make sense against the mini tokenizer's own added
# tokens (overlapping, multi-byte and space-bearing contents).
MINI_ADDED = [
    AddedToken("<|pad|>", normalized=False, special=True),
    AddedToken("<|im_start|>", normalized=False, special=True),
    AddedToken("<|im_end|>", normalized=False, special=True),
    AddedToken("<|voice|>", normalized=False, special=True),
    AddedToken(">>ABSTRACT<<", normalized=False, special=True),
    AddedToken("<|a|>", normalized=False, special=True),
    AddedToken("<|a|>b", normalized=False, special=True),
    AddedToken("\\blindtext", normalized=False, special=False),
    AddedToken("ab", normalized=False, special=False),
    AddedToken("abcd", normalized=False, special=False),
    AddedToken("日本", normalized=False, special=False),
    AddedToken("a b", normalized=False, special=False),
]

MINI_EXTRA_CASES = [
    "ab",
    "abc",
    "abcd",
    "abcde",
    "xabcdy",
    "<|a|>",
    "<|a|>b",
    "<|a|>bc",
    "z<|a|>b",
    "日本",
    "日本語",
    "a b",
    "a  b",
    "\\blindtext and \\blindtext",
    "abab",
    "ab abcd ab",
]

TRAIN_TEXT = [
    "the quick brown fox jumps over the lazy dog and keeps running",
    "she sells sea shells by the sea shore while the sun sets slowly",
    "convert the provided text to speech reference to the following text speech",
    "it continues here then a third thought arrives and another one after that",
    "hello world hello there hello again and welcome back to the machine",
    "日本語のテキストと漢字とひらがなとカタカナの混合したテキスト",
    "至今为止元气火箭总共发行了两张专辑中文简体和繁體中文",
    "naïve café übermäßig große änderungen el niño está aquí",
    "съешь же ещё этих мягких булок γειά σου κόσμε",
    "numbers 0 1 2 3 4 5 6 7 8 9 and 1984 2026 3.14159 100% $1,234.56",
    "punctuation , . ! ? ; : ' \" ( ) [ ] { } - _ / \\ @ # & * + = < > | ~ `",
]


def load_reference(model_dir: Path) -> tuple[Tokenizer, Path]:
    path = model_dir / "tokenizer" / "tokenizer.json"
    if not path.exists():
        path = model_dir / "tokenizer.json"
    if not path.exists():
        sys.exit(f"no tokenizer.json under {model_dir}")
    return Tokenizer.from_file(str(path)), path


def encode(tok: Tokenizer, text: str) -> list[int]:
    # add_special_tokens=False mirrors the vendor's PromptBuilder.encode_text.
    # With a null post-processor it is a no-op, which this asserts.
    ids = tok.encode(text, add_special_tokens=False).ids
    assert ids == tok.encode(text, add_special_tokens=True).ids, text
    return list(ids)


def cases_for(tok: Tokenizer, texts: list[str]) -> list[dict]:
    return [{"text": t, "ids": encode(tok, t)} for t in texts]


def write(path: Path, doc: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(doc, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    print(f"wrote {path.relative_to(REPO)}")


def sweep_punctuation() -> list[list[int]]:
    """Every code point the Punctuation pre-tokenizer splits on, as ranges."""
    from tokenizers import pre_tokenizers

    punct = pre_tokenizers.Punctuation(behavior="contiguous")
    hits: list[int] = []
    for cp in range(0x110000):
        if 0xD800 <= cp <= 0xDFFF or cp in (ord("A"), ord("a")):
            continue
        if len(punct.pre_tokenize_str("A" + chr(cp) + "A")) == 3:
            hits.append(cp)
    ranges: list[list[int]] = []
    for cp in hits:
        if ranges and cp == ranges[-1][1] + 1:
            ranges[-1][1] = cp
        else:
            ranges.append([cp, cp])
    return ranges


def build_mini(reference: Tokenizer) -> Tokenizer:
    from tokenizers import pre_tokenizers

    mini = Tokenizer(models.BPE())
    mini.pre_tokenizer = reference.pre_tokenizer
    trainer = trainers.BpeTrainer(
        vocab_size=1200,
        min_frequency=1,
        show_progress=False,
        initial_alphabet=pre_tokenizers.ByteLevel.alphabet(),
        special_tokens=[],
    )
    mini.train_from_iterator(TRAIN_TEXT + corpus.all_cases(), trainer=trainer)
    mini.add_tokens([t for t in MINI_ADDED if not t.special])
    mini.add_special_tokens([t for t in MINI_ADDED if t.special])
    return mini


def main() -> None:
    model_dir = os.environ.get("AUDIO8_MODEL_DIR")
    if not model_dir:
        sys.exit("set AUDIO8_MODEL_DIR to the Audio8 snapshot (see scripts/audio8_bench/README.md)")
    reference, path = load_reference(Path(model_dir))
    digest = hashlib.sha256(path.read_bytes()).hexdigest()

    texts = corpus.all_cases()
    write(
        OUT / "audio8_parity.json",
        {
            "note": "generated by scripts/audio8_tokenizer/gen_parity.py from the checkpoint's own tokenizer.json",
            "tokenizer_sha256": digest,
            "cases": cases_for(reference, texts),
        },
    )

    # The integration fixture and the parity corpus must be the same tokenizer.
    fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
    for text, ids in fixture["encodings"].items():
        got = encode(reference, text)
        if got != list(ids):
            sys.exit(f"fixture disagrees with the reference for {text!r}: {ids} vs {got}")
    print(f"fixture: {len(fixture['encodings'])} entries agree with the reference")

    write(
        OUT / "punctuation_codepoints.json",
        {
            "note": "code points tokenizers' Punctuation pre-tokenizer splits on, swept one per call",
            "ranges": sweep_punctuation(),
        },
    )

    mini = build_mini(reference)
    mini_path = OUT / "mini_tokenizer.json"
    mini.save(str(mini_path), pretty=True)
    print(f"wrote {mini_path.relative_to(REPO)}")
    mini = Tokenizer.from_file(str(mini_path))
    write(
        OUT / "mini_parity.json",
        {
            "note": "same corpus through mini_tokenizer.json; runs without the checkpoint",
            "cases": cases_for(mini, texts + MINI_EXTRA_CASES),
        },
    )


if __name__ == "__main__":
    main()
