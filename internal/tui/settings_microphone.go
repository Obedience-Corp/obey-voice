package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

type microphoneSwitchedMsg struct {
	runtime     *ConversationRuntime
	name        string
	err         error
	resumeVoice bool
	reloadVoice bool
}

func switchMicrophone(ctx context.Context, rt *ConversationRuntime, name string, resume, reload bool) tea.Cmd {
	return func() tea.Msg {
		msg := microphoneSwitchedMsg{runtime: rt, name: name, resumeVoice: resume, reloadVoice: reload}
		if rt.SetInputDevice == nil {
			msg.err = fmt.Errorf("live microphone switching is unavailable")
		} else {
			msg.err = rt.SetInputDevice(ctx, name)
		}
		return msg
	}
}
