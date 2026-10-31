import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("comparison", Path(__file__).with_name("compare-audio-recognition.py"))
comparison = importlib.util.module_from_spec(spec)
spec.loader.exec_module(comparison)


class RecognitionEvidenceTests(unittest.TestCase):
    def test_repeated_words_and_fillers_are_not_deduplicated(self):
        expected = comparison.tokens("Um, I I like it.")
        actual = comparison.tokens("I like it.")
        result = comparison.score(expected, actual)
        self.assertEqual(result["word_edits"], 2)
        self.assertEqual(result["word_edit_rate"], .4)
        self.assertEqual(result["reference_fillers"], 1)
        self.assertEqual(result["predicted_fillers"], 0)

    def test_reference_free_hallucinations_are_not_zero_error(self):
        result = comparison.score([], ["invented", "speech"])
        self.assertEqual(result["word_edits"], 2)
        self.assertIsNone(result["word_edit_rate"])
        self.assertEqual(result["predicted_words"], 2)

    def test_long_window_plan_retains_tail_and_overlap(self):
        windows = list(comparison.windows(90*16000 + 7))
        self.assertEqual(windows, [(0, 480000), (448000, 928000),
                                   (896000, 1376000), (1344000, 1440007)])

    def test_failed_partial_stream_and_wrong_terminal_cannot_be_scored(self):
        with tempfile.TemporaryDirectory() as temporary:
            prefix = Path(temporary) / "worker"
            for code, terminal in [(1, {"type": "failed", "job_id": "expected"}),
                                   (0, {"type": "completed", "job_id": "another"})]:
                events = [{"type": "segment", "words": [{"text": "apparently valid"}]}, terminal]
                prefix.with_suffix(".jsonl").write_text("\n".join(json.dumps(event) for event in events))
                with self.assertRaises(ValueError):
                    comparison.completed(prefix, {"exit_code": code, "timed_out": False}, "expected")


if __name__ == "__main__":
    unittest.main()
