package anim

import (
	"fmt"
	"math"
	"strings"
)

// Progress bars for waits.
//
// Two kinds, and the difference is honest rather than cosmetic: Bar knows how
// far along it is, Sweep does not. A model loading into memory reports no
// percentage, so it gets a travelling highlight instead of a filled bar that
// would have to invent one.

const (
	barFull  = "█"
	barTrack = "░"
)

// Bar renders a determinate bar for pct (0-100) with the number beside it.
func Bar(pct float64, width int, s Styles) string {
	if width < 8 {
		width = 8
	}
	pct = math.Max(0, math.Min(100, pct))
	// The percentage costs five columns: " 100%".
	track := width - 5
	filled := int(math.Round(float64(track) * pct / 100))
	return s.Accent.Render(strings.Repeat(barFull, filled)) +
		s.Muted.Render(strings.Repeat(barTrack, track-filled)) +
		s.Muted.Render(fmt.Sprintf(" %3d%%", int(math.Round(pct))))
}

// Sweep renders an indeterminate bar: a lit segment travelling a dim track,
// turning back at each end so it reads as work rather than as progress.
// Reduced motion gets the track alone.
func Sweep(frame, width int, s Styles, reduced bool) string {
	if width < 8 {
		width = 8
	}
	if reduced {
		return s.Muted.Render(strings.Repeat(barTrack, width))
	}
	seg := max(width/6, 3)
	span := width - seg
	if span <= 0 {
		return s.Accent.Render(strings.Repeat(barFull, width))
	}
	// Triangle wave: 0 → span → 0.
	pos := frame % (2 * span)
	if pos > span {
		pos = 2*span - pos
	}
	return s.Muted.Render(strings.Repeat(barTrack, pos)) +
		s.Accent.Render(strings.Repeat(barFull, seg)) +
		s.Muted.Render(strings.Repeat(barTrack, span-pos))
}
