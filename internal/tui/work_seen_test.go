package tui

import (
	"maps"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// visibleResultsByRendering is the reference: render every item now, wrap it
// alone and report the results whose rows meet the viewport.
func visibleResultsByRendering(m conversationModel) []string {
	var ids []string
	start, end, line := m.viewport.YOffset, m.viewport.YOffset+m.viewport.Height, 0
	for _, item := range m.items {
		rendered := item.render(m.showThink, m.viewport.Width)
		if rendered == "" {
			continue
		}
		h := lipgloss.Height(lipgloss.NewStyle().Width(max(m.viewport.Width, 1)).Render(rendered))
		if item.workResultID != "" && line < end && line+h > start {
			ids = append(ids, item.workResultID)
		}
		line += h
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func visibleResultsMarked(m conversationModel) []string {
	m.workDisplay.seen = map[string]bool{}
	m.markVisibleWorkResults()
	return slices.Sorted(maps.Keys(m.workDisplay.seen))
}

func TestWorkSeenMatchesRenderedRowsAcrossScrollResizeAndThinking(t *testing.T) {
	truecolorTB(t)
	m := longConversation(t, 240, 100, 30)
	check := func(step string) {
		t.Helper()
		want, got := visibleResultsByRendering(m), visibleResultsMarked(m)
		if !slices.Equal(want, got) {
			t.Fatalf("%s (offset %d of %d): marked %v, on screen %v", step, m.viewport.YOffset, m.viewport.TotalLineCount(), got, want)
		}
	}
	sweep := func(step string) {
		t.Helper()
		hits := 0
		m.viewport.GotoTop()
		for {
			check(step)
			if len(visibleResultsByRendering(m)) > 0 {
				hits++
			}
			if m.viewport.AtBottom() {
				break
			}
			m.viewport.ScrollDown(7)
		}
		if hits == 0 {
			t.Fatalf("%s: no step showed a result", step)
		}
	}
	sweep("100 columns")
	for _, size := range [][2]int{{61, 24}, {33, 40}, {140, 20}} {
		m, _ = m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		sweep("resized")
	}
	m.setShowThink(true)
	sweep("thinking shown")
	m.setShowThink(false)
	sweep("thinking hidden")
}

func TestWorkSeenOnlyAfterOnScreenThroughScrollAndRewrap(t *testing.T) {
	truecolorTB(t)
	m := longConversation(t, 400, 120, 30)
	for range 3 {
		m, _ = m.Update(tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	}
	if len(m.workDisplay.seen) != 0 {
		t.Fatalf("results above the fold marked seen: %v", m.workDisplay.seen)
	}
	if m.workCounts().unseen != 3 {
		t.Fatalf("unseen = %d, want 3", m.workCounts().unseen)
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	if len(m.workDisplay.seen) != 0 {
		t.Fatalf("rewrap marked results seen while off screen: %v", m.workDisplay.seen)
	}
	for !m.viewport.AtBottom() && len(m.workDisplay.seen) == 0 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		if got, want := visibleResultsMarked(m), visibleResultsByRendering(m); !slices.Equal(got, want) {
			t.Fatalf("offset %d: marked %v, on screen %v", m.viewport.YOffset, got, want)
		}
	}
	if len(m.workDisplay.seen) == 0 {
		t.Fatal("paging through the results never marked one seen")
	}
	for id := range m.workDisplay.seen {
		if !slices.Contains(visibleResultsByRendering(m), id) {
			t.Fatalf("%s marked seen without being on screen", id)
		}
	}
}

func TestScrollDoesNotRenderTranscriptItems(t *testing.T) {
	truecolorTB(t)
	m := longConversation(t, 2000, 120, 40)
	if len(m.workResultRows) != 3 {
		t.Fatalf("result rows = %d, want 3", len(m.workResultRows))
	}
	for _, msg := range []tea.Msg{
		tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress},
		tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress},
		tea.KeyMsg{Type: tea.KeyPgDown},
		tea.KeyMsg{Type: tea.KeyPgUp},
	} {
		before := chatItemRenders.Load()
		m, _ = m.Update(msg)
		if n := chatItemRenders.Load() - before; n != 0 {
			t.Fatalf("%T rendered %d transcript items", msg, n)
		}
	}
}
