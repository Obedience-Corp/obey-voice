package anim

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func plainStyles() Styles {
	return Styles{Accent: lipgloss.NewStyle(), Label: lipgloss.NewStyle(), Muted: lipgloss.NewStyle()}
}

func TestOrbFillsItsBox(t *testing.T) {
	for _, rows := range []int{4, 5, 7, 9, 11, 15} {
		lines := Orb(0, rows, plainStyles(), false)
		if len(lines) != rows {
			t.Fatalf("rows=%d: got %d lines", rows, len(lines))
		}
		for i, line := range lines {
			if got := len([]rune(line)); got > OrbWidth(rows) {
				t.Fatalf("rows=%d line %d is %d cells, wider than OrbWidth %d", rows, i, got, OrbWidth(rows))
			}
		}
	}
}

// Every ring has a gap — that is what makes the figure the mark and not a set
// of closed circles — so the drawing must never fill its own bounding box.
func TestOrbIsRoundAndGapped(t *testing.T) {
	lines := Orb(0, 11, plainStyles(), false)
	first, last := lines[0], lines[len(lines)-1]
	if strings.TrimSpace(first) == "" || strings.TrimSpace(last) == "" {
		t.Fatal("orb does not reach the top and bottom of its box")
	}
	// Corners stay empty: a circle, not a square.
	for _, line := range []string{lines[0], lines[len(lines)-1]} {
		runes := []rune(line)
		if len(runes) > 0 && runes[0] != ' ' {
			t.Fatalf("corner is inked, so the figure is not round: %q", line)
		}
	}
	var blanks int
	for _, line := range lines {
		blanks += strings.Count(line, " ")
	}
	if blanks == 0 {
		t.Fatal("orb has no gaps at all")
	}
}

// The rings turn, and each at its own rate: successive frames differ.
func TestOrbRotates(t *testing.T) {
	a := strings.Join(Orb(0, 11, plainStyles(), false), "\n")
	b := strings.Join(Orb(6, 11, plainStyles(), false), "\n")
	if a == b {
		t.Fatal("orb is identical across frames; nothing is turning")
	}
}

func TestOrbHoldsStillUnderReducedMotion(t *testing.T) {
	a := strings.Join(Orb(0, 11, plainStyles(), true), "\n")
	b := strings.Join(Orb(37, 11, plainStyles(), true), "\n")
	if a != b {
		t.Fatal("reduced motion still animates the orb")
	}
	if strings.TrimSpace(a) == "" {
		t.Fatal("reduced motion rendered nothing at all")
	}
}

func TestBarFillsByPercent(t *testing.T) {
	for _, tc := range []struct {
		pct   float64
		full  int
		label string
	}{
		{0, 0, "  0%"},
		{50, 10, " 50%"},
		{100, 20, "100%"},
		{-5, 0, "  0%"},   // clamped
		{140, 20, "100%"}, // clamped
	} {
		got := Bar(tc.pct, 25, plainStyles())
		if n := strings.Count(got, barFull); n != tc.full {
			t.Errorf("Bar(%v) filled %d cells, want %d (%q)", tc.pct, n, tc.full, got)
		}
		if !strings.Contains(got, tc.label) {
			t.Errorf("Bar(%v) = %q, want it to read %q", tc.pct, got, tc.label)
		}
	}
}

// The indeterminate bar travels and turns back; it never claims a percentage.
func TestSweepTravelsWithoutClaimingProgress(t *testing.T) {
	seen := map[int]bool{}
	for frame := range 40 {
		got := Sweep(frame, 30, plainStyles(), false)
		if strings.Contains(got, "%") {
			t.Fatalf("indeterminate bar shows a percentage: %q", got)
		}
		if len([]rune(got)) != 30 {
			t.Fatalf("frame %d: width %d, want 30", frame, len([]rune(got)))
		}
		seen[strings.Index(got, barFull)] = true
	}
	if len(seen) < 5 {
		t.Fatalf("sweep only reached %d positions; it is not travelling", len(seen))
	}
	still := Sweep(9, 30, plainStyles(), true)
	if strings.Contains(still, barFull) {
		t.Fatalf("reduced motion still lights the sweep: %q", still)
	}
}
