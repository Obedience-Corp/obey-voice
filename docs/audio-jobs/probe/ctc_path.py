"""Bounded diagnostic CTC Viterbi alignment, independent of inference/runtime.

The blank-expanded target automaton permits stay, advance and nonrepeating
label-skip transitions. No target token is dropped, merged or interpolated.
Frame log probabilities are path scores, never calibrated word confidence.
"""
import numpy as np


def forced_path(emissions, targets, blank=0):
    emissions = np.asarray(emissions, dtype=np.float64)
    if emissions.ndim != 2 or not emissions.shape[0] or not targets:
        raise ValueError("nonempty frames and targets are required")
    frames, vocabulary = emissions.shape
    if not 0 <= blank < vocabulary or any(
        not isinstance(token, (int, np.integer)) or token == blank
        or not 0 <= token < vocabulary for token in targets
    ):
        raise ValueError("invalid CTC target/blank ID")
    if np.isnan(emissions).any() or np.isposinf(emissions).any():
        raise ValueError("invalid emission score")
    minimum = len(targets) + sum(a == b for a, b in zip(targets, targets[1:]))
    if frames < minimum:
        raise ValueError("not enough frames for repeated CTC targets")
    states = 2 * len(targets) + 1
    if frames * states > 20_000_000:
        raise ValueError("diagnostic alignment exceeds its frame/target budget")
    labels = np.full(states, blank, dtype=np.int64)
    labels[1::2] = targets
    skip = np.zeros(states, dtype=bool)
    skip[2:] = (labels[2:] != blank) & (labels[2:] != labels[:-2])
    previous = np.full(states, -np.inf)
    previous[0] = emissions[0, blank]
    previous[1] = emissions[0, targets[0]]
    back = np.zeros((frames, states), dtype=np.uint8)
    columns = np.arange(states)
    for frame in range(1, frames):
        advance = np.full(states, -np.inf)
        advance[1:] = previous[:-1]
        leap = np.full(states, -np.inf)
        leap[2:] = np.where(skip[2:], previous[:-2], -np.inf)
        choices = np.stack((previous, advance, leap))
        selected = choices.argmax(axis=0)
        previous = choices[selected, columns] + emissions[frame, labels]
        back[frame] = selected
    final = states - 1 if previous[-1] >= previous[-2] else states - 2
    score = previous[final]
    if not np.isfinite(score):
        raise ValueError("no complete CTC path for supplied transcript")
    path = np.empty(frames, dtype=np.int64)
    state = final
    for frame in range(frames - 1, -1, -1):
        path[frame] = state
        if frame:
            state -= int(back[frame, state])
    spans = []
    for index in range(len(targets)):
        selected = np.flatnonzero(path == 2 * index + 1)
        if not len(selected):
            raise ValueError("complete path omitted a target token")
        spans.append((int(selected[0]), int(selected[-1]) + 1))
    return spans, float(score)
