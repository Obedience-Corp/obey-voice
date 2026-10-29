package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *conversationModel) mediaInputCommand() (tea.Cmd, bool) {
	text := strings.TrimSpace(m.input.Value())
	if text == "/attach" || strings.HasPrefix(text, "/attach ") {
		paths, ok := chatmedia.Paths(strings.TrimSpace(strings.TrimPrefix(text, "/attach ")))
		if !ok {
			m.commandError("Usage: /attach /path/to/image.png (PNG, JPEG or GIF)")
			return nil, true
		}
		return m.importMediaDraft(paths), true
	}
	if text == "/image" || strings.HasPrefix(text, "/image ") {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(text, "/image ")))
		var refs []chatmedia.Ref
		for _, item := range m.items {
			refs = append(refs, item.attachments...)
		}
		if err != nil || n < 1 || n > len(refs) {
			m.commandError(fmt.Sprintf("Usage: /image N (1–%d)", len(refs)))
			return nil, true
		}
		m.input.Reset()
		m.syncEditorFromTextarea()
		return openMediaFile(chatmedia.DefaultStore().Path(refs[n-1])), true
	}
	if paths, ok := chatmedia.Paths(text); ok {
		return m.importMediaDraft(paths), true
	}
	return nil, false
}
func (m *conversationModel) importMediaDraft(paths []string) tea.Cmd {
	cmd := m.importMedia(paths, nil)
	if cmd == nil {
		return nil
	}
	text := m.input.Value()
	return func() tea.Msg { msg := cmd().(mediaImported); msg.clearText = text; return msg }
}
