package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestLauncherAnimationLifecycle(t *testing.T) {
	for _, key := range []string{"OBEY_VOICE_REDUCED_MOTION", "NO_MOTION", "FESTIVAL_REDUCED_MOTION"} {
		t.Setenv(key, "0")
	}
	a := newApp(&config.Config{}, nil, nil)
	// App geometry alone is insufficient: only the launcher's rendered mark
	// should arm a timer, including before its first size message arrives.
	a.width, a.height = 100, 32
	if cmd := a.syncLauncherAnimation(); cmd != nil {
		t.Fatal("unsized launcher started an invisible animation")
	}
	update := func(msg tea.Msg) tea.Cmd {
		t.Helper()
		model, cmd := a.Update(msg)
		a = model.(App)
		return cmd
	}
	if cmd := update(tea.WindowSizeMsg{Width: 100, Height: 32}); cmd == nil || !a.launcherTicking {
		t.Fatal("initial terminal size did not start animation")
	}
	first := stripANSI(a.View())
	tick := launcherTickMsg{id: a.launcherTickID}
	if cmd := update(tick); cmd == nil || a.launcher.logoFrame != 1 || stripANSI(a.View()) == first {
		t.Fatal("current tick did not animate and schedule its successor")
	}
	if cmd := update(tick); cmd != nil || a.launcher.logoFrame != 1 {
		t.Fatal("duplicate tick advanced the logo or created another timer")
	}
	if cmd := update(tea.KeyMsg{Type: tea.KeyDown}); cmd != nil || a.launcher.cursor != 1 {
		t.Fatal("navigation failed or scheduled a second timer")
	}
	old := launcherTickMsg{id: a.launcherTickID}
	update(switchScreenMsg(screenSessions))
	if a.launcherTicking {
		t.Fatal("hidden launcher kept ticking")
	}
	if cmd := update(old); cmd != nil || a.launcher.logoFrame != 1 {
		t.Fatal("hidden launcher consumed its old tick")
	}
	if cmd := update(switchScreenMsg(screenLauncher)); cmd == nil || !a.launcherTicking {
		t.Fatal("returning to the launcher did not resume animation")
	}
	if cmd := update(old); cmd != nil || a.launcher.logoFrame != 1 {
		t.Fatal("stale tick survived re-entry")
	}
	old = launcherTickMsg{id: a.launcherTickID}
	if cmd := update(tea.WindowSizeMsg{Width: 50, Height: 20}); cmd != nil || a.launcherTicking {
		t.Fatal("compact launcher kept a logo timer")
	}
	if cmd := update(tea.WindowSizeMsg{Width: 80, Height: 24}); cmd == nil || !a.launcherTicking {
		t.Fatal("restoring logo space did not restart animation")
	}
	if cmd := update(old); cmd != nil || a.launcher.logoFrame != 1 {
		t.Fatal("stale tick survived resize")
	}
	old = launcherTickMsg{id: a.launcherTickID}
	update(quitMsg{})
	if a.launcherTicking {
		t.Fatal("quitting kept a logo timer")
	}
	if cmd := update(old); cmd != nil || a.launcher.logoFrame != 1 {
		t.Fatal("tick after quit restarted animation")
	}
}

func TestLauncherAnimationKeepsLayout(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {100, 32}} {
		m := newLauncher(&config.Config{}, nil)
		m.reducedMotion = false
		m.width, m.height = size.width, size.height
		first := stripANSI(m.View())
		plateLines := len(strings.Split(m.brandPlate(m.width), "\n"))
		menu := strings.Join(strings.Split(first, "\n")[plateLines:], "\n")
		for _, frame := range []int{12, 30, 100, 3599} {
			m.logoFrame = frame
			view := stripANSI(m.View())
			if view == first {
				t.Fatalf("logo did not move at frame %d, size %+v", frame, size)
			}
			if lipgloss.Width(view) != lipgloss.Width(first) || lipgloss.Height(view) != lipgloss.Height(first) {
				t.Fatalf("animation changed layout at frame %d, size %+v", frame, size)
			}
			if strings.Join(strings.Split(view, "\n")[plateLines:], "\n") != menu {
				t.Fatal("animation moved or changed the menu below the logo")
			}
		}
	}
}

func TestLauncherReducedMotionDoesNotTick(t *testing.T) {
	t.Setenv("OBEY_VOICE_REDUCED_MOTION", "1")
	a := newApp(&config.Config{}, nil, nil)
	model, cmd := a.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	a = model.(App)
	if cmd != nil || a.launcherTicking {
		t.Fatal("reduced motion started a timer")
	}
	first := a.View()
	a.launcher.logoFrame = 50
	if a.View() != first {
		t.Fatal("reduced motion changed the logo")
	}
}
