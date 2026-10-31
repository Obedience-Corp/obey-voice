import importlib.util
from pathlib import Path
import sys
import unittest

probe = Path(__file__).resolve().parents[1] / "docs/audio-jobs/probe"
sys.path.insert(0, str(probe))
spec = importlib.util.spec_from_file_location("alignment", probe / "compare_alignment.py")
alignment = importlib.util.module_from_spec(spec)
spec.loader.exec_module(alignment)


class AlignmentEvidenceTests(unittest.TestCase):
    def test_zero_duration_is_invalid_even_when_text_and_start_match(self):
        reference = [{"text": "um", "start_us": 100, "end_us": 300}]
        words = [{"text": "um", "start_us": 100, "end_us": 100}]
        result = alignment.measurements(reference, words, "um", 1000)
        self.assertEqual(result["invalid_edge_indices"], [0])
        self.assertFalse(result["geometry_valid"])
        self.assertFalse(result["batch_qualified"])

    def test_dropped_repeat_cannot_be_hidden_by_monotonic_timestamps(self):
        reference = [{"text": "I", "start_us": 0, "end_us": 100},
                     {"text": "I", "start_us": 200, "end_us": 300}]
        result = alignment.measurements(reference, reference[:1], "I I", 1000)
        self.assertFalse(result["supplied_tokens_preserved"])
        self.assertFalse(result["geometry_valid"])
        self.assertEqual(result["reference_words"], 2)
        self.assertEqual(result["returned_words"], 1)

    def test_far_away_match_is_counted_as_unmatched_not_zero_error(self):
        reference = [{"text": "um", "start_us": 0, "end_us": 100}]
        words = [{"text": "um", "start_us": 2_000_000, "end_us": 2_100_000}]
        result = alignment.measurements(reference, words, "um", 3_000_000)
        self.assertEqual(result["matched_words_within_one_second"], 0)
        self.assertIsNone(result["all_matched_edge_error"])
        self.assertTrue(result["geometry_valid"])
        self.assertEqual(result["timing_validity"], "unqualified")


if __name__ == "__main__":
    unittest.main()
