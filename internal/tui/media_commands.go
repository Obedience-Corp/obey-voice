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
		return m.imageCommand(strings.TrimSpace(strings.TrimPrefix(text, "/image"))), true
	}
	if paths, ok := imagePaths(text); ok {
		return m.importMediaDraft(paths), true
	}
	return nil, false
}

// imageCommand runs /image: with no number it opens the picture panel on the
// latest picture, with N it opens picture N in the desktop viewer.
func (m *conversationModel) imageCommand(arg string) tea.Cmd {
	spots := m.pictureSpots()
	if len(spots) == 0 {
		m.commandError("No picture in this conversation yet")
		return nil
	}
	if arg == "" {
		previous := m.input.Value()
		m.input.Reset()
		m.editor.sync("", 0)
		m.editor.resetUndo()
		m.syncComposer(previous)
		return m.openPicturePanel(spots[len(spots)-1])
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > len(spots) {
		m.commandError(fmt.Sprintf("Usage: /image for the latest picture's actions, /image N (1–%d) to open picture N", len(spots)))
		return nil
	}
	m.input.Reset()
	m.syncEditorFromTextarea()
	return m.openPictureCmd(chatmedia.DefaultStore().Path(spots[n-1].ref))
}

func (m *conversationModel) importMediaDraft(paths []string) tea.Cmd {
	cmd := m.importMedia(paths, nil)
	if cmd == nil {
		return nil
	}
	text := m.input.Value()
	return func() tea.Msg { msg := cmd().(mediaImported); msg.clearText = text; return msg }
}
