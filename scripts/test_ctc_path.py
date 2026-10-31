import importlib.util
import itertools
from pathlib import Path
import unittest

import numpy as np

spec = importlib.util.spec_from_file_location(
    "ctc_path", Path(__file__).resolve().parents[1] / "docs/audio-jobs/probe/ctc_path.py")
ctc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ctc)


def collapsed(path):
    return [token for index, token in enumerate(path)
            if token and (index == 0 or token != path[index-1])]


class CTCPathTests(unittest.TestCase):
    def test_scores_match_exhaustive_valid_paths_including_repeated_labels(self):
        emissions = np.log(np.array([[.2, .7, .1], [.6, .1, .3],
                                     [.2, .6, .2], [.2, .2, .6]]))
        for target in ([1], [1, 1], [1, 2], [2, 1], [1, 2, 1]):
            paths = [path for path in itertools.product(range(3), repeat=4)
                     if collapsed(path) == list(target)]
            expected = max(sum(emissions[i, token] for i, token in enumerate(path))
                           for path in paths)
            spans, actual = ctc.forced_path(emissions, target)
            self.assertAlmostEqual(actual, expected)
            self.assertEqual(len(spans), len(target))
            self.assertTrue(all(start < end for start, end in spans))
            self.assertTrue(all(a[1] <= b[0] for a, b in zip(spans, spans[1:])))

    def test_repeated_target_needs_a_real_separating_blank_frame(self):
        with self.assertRaises(ValueError):
            ctc.forced_path(np.zeros((2, 3)), [1, 1])
        emissions = np.full((3, 3), -np.inf)
        emissions[0, 1] = emissions[1, 0] = emissions[2, 1] = 0
        self.assertEqual(ctc.forced_path(emissions, [1, 1])[0], [(0, 1), (2, 3)])

    def test_impossible_or_invalid_inputs_never_return_partial_alignment(self):
        for emissions, target in [(np.full((4, 3), -np.inf), [1]),
                                  (np.zeros((2, 3)), [0]),
                                  (np.array([[np.nan, 0, 0]]), [1]),
                                  (np.zeros((2, 3)), [3])]:
            with self.assertRaises(ValueError):
                ctc.forced_path(emissions, target)


if __name__ == "__main__":
    unittest.main()
