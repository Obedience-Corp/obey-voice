package anim

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The Obedience presence mark, drawn in terminal cells.
//
// Geometry is the brand orb from the presence-orb spec (512×512 viewBox, outer
// radius 200): a dark core, an inner cyan C with its gap, a white C, and two
// outer cyan arcs. The radii below are those numbers normalized by the outer
// edge, so the terminal orb and the app's orb are the same drawing at
// different resolutions.
//
// It is drawn on a braille canvas — 2×4 dots per cell — for two reasons: a
// terminal cell is about twice as tall as it is wide, which makes braille dots
// very nearly square, and four concentric rings need more vertical resolution
// than one row per ring.

// orbRing is one concentric arc: an annular band, an angular sweep, and how
// fast its gap travels around the circle. The band is what makes the figure
// read as the mark rather than as wireframe circles — the brand rings are
// thick, roughly a seventh of the radius each.
type orbRing struct {
	inner, outer float64 // normalized 0-1 against the outer edge
	sweep        float64 // degrees of circle drawn
	start        float64 // degrees at frame 0
	speed        float64 // degrees per frame; sign sets direction
	ink          orbInk
}

// orbInk selects a ring's colour without anim reaching into the palette twice.
type orbInk int

const (
	inkCore orbInk = iota
	inkAccent
	inkWhite
)

// orbRings is the mark: L1 inner C, L2 white C, L3 + L4 outer arcs. Each turns
// at its own rate and direction, so the figure reads as working rather than as
// one spinning wheel. L2 is the slow white rotation the spec gives `starting`.
var orbRings = []orbRing{
	{inner: 0.318, outer: 0.460, sweep: 308, start: 20, speed: 2.4, ink: inkAccent},
	{inner: 0.586, outer: 0.728, sweep: 345, start: 200, speed: -1.1, ink: inkWhite},
	{inner: 0.854, outer: 1.000, sweep: 212, start: 90, speed: 1.7, ink: inkAccent},
	{inner: 0.854, outer: 1.000, sweep: 126, start: 315, speed: 1.7, ink: inkAccent},
}

// orbCoreRadius is L0, the dark disc at the centre.
const orbCoreRadius = 0.209

// brailleBits maps a dot's (column, row) inside a cell to its braille bit.
var brailleBits = [2][4]rune{
	{0x01, 0x02, 0x04, 0x40},
	{0x08, 0x10, 0x20, 0x80},
}

// Orb renders the presence mark as rows lines of braille (each OrbWidth(rows)
// cells wide). frame advances the rotation; reduced holds the figure still.
func Orb(frame, rows int, s Styles, reduced bool) []string {
	rows = orbRows(rows)
	cols := OrbWidth(rows)

	dotW, dotH := cols*2, rows*4
	cx, cy := float64(dotW-1)/2, float64(dotH-1)/2
	// One dot of margin keeps the outer arcs off the edge at every size.
	scale := float64(min(dotW, dotH))/2 - 1

	bits := make([][]rune, rows)
	ink := make([][][3]uint16, rows)
	for y := range bits {
		bits[y] = make([]rune, cols)
		ink[y] = make([][3]uint16, cols)
	}
	setDot := func(dx, dy int, kind orbInk) {
		if dx < 0 || dy < 0 || dx >= dotW || dy >= dotH {
			return
		}
		cellX, cellY := dx/2, dy/4
		bits[cellY][cellX] |= brailleBits[dx%2][dy%4]
		ink[cellY][cellX][kind]++
	}

	// Core: a filled disc, dimmer than the rings around it.
	core := orbCoreRadius * scale
	for dy := 0; dy < dotH; dy++ {
		for dx := 0; dx < dotW; dx++ {
			if math.Hypot(float64(dx)-cx, float64(dy)-cy) <= core {
				setDot(dx, dy, inkCore)
			}
		}
	}

	// Rings, sampled by angle so an arc stays continuous at any size.
	for _, ring := range orbRings {
		start := ring.start
		if !reduced {
			start += float64(frame) * ring.speed
		}
		inner, outer := ring.inner*scale, ring.outer*scale
		steps := int(math.Max(64, 4*math.Pi*outer))
		for i := 0; i <= steps; i++ {
			rad := (start + ring.sweep*float64(i)/float64(steps)) * math.Pi / 180
			sin, cos := math.Sincos(rad)
			// Half-dot steps across the band: a whole-dot walk leaves seams
			// where the band runs diagonally through the grid.
			for r := inner; r <= outer; r += 0.5 {
				setDot(int(math.Round(cx+r*cos)), int(math.Round(cy+r*sin)), ring.ink)
			}
		}
	}

	palette := [3]lipgloss.Style{s.Muted, s.Accent, s.Label}
	lines := make([]string, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		for x := 0; x < cols; x++ {
			if bits[y][x] == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(palette[dominantInk(ink[y][x])].Render(string(0x2800 + bits[y][x])))
		}
		lines[y] = b.String()
	}
	return lines
}

// dominantInk colours a cell by the ring that put the most dots in it, so a
// cell where two rings meet takes the colour of the one that owns it.
func dominantInk(votes [3]uint16) orbInk {
	best := inkCore
	for kind := inkAccent; kind <= inkWhite; kind++ {
		if votes[kind] > votes[best] {
			best = kind
		}
	}
	return best
}

// OrbWidth is the rendered width, in cells, of an Orb with this many rows.
// The canvas is kept square in dots: 2 dots per cell across, 4 down.
func OrbWidth(rows int) int { return orbRows(rows) * 2 }

// orbRows clamps the requested height to what the figure can be drawn in.
func orbRows(rows int) int {
	if rows < 4 {
		return 4
	}
	return rows
}
