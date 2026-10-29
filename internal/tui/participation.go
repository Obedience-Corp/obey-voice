package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *conversationModel) runParticipationCommand(args []string) tea.Cmd {
	rt := m.selectionRuntime
	if rt == nil || rt.Participation == nil || rt.SetParticipation == nil {
		m.commandError("group participation is unavailable in this conversation")
		return nil
	}
	settings := rt.Participation()
	if len(args) == 0 {
		members := "all enabled personas"
		if settings.Members != nil {
			members = strings.Join(settings.Members, ", ")
		}
		m.emit(events.Info{Message: fmt.Sprintf("Agent participation: %s; members: %s", settings.Mode, members)})
		return m.resumeListening()
	}
	if len(args) > 2 {
		m.commandError("usage: /participation [off|invited|open] [all|id,id]")
		return nil
	}
	settings.Mode = args[0]
	if len(args) == 2 {
		settings.Members = nil
		if args[1] != "all" {
			settings.Members = strings.Split(args[1], ",")
		}
	}
	if err := settings.Validate(); err != nil {
		m.commandError(err.Error())
		return nil
	}
	ctx := m.deps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		if err := rt.SetParticipation(ctx, settings); err != nil {
			m.emit(events.Error{Stage: "participation", Message: err.Error()})
		} else {
			m.emit(events.Info{Message: "Agent participation: " + settings.Mode})
		}
		return nil
	}
}
