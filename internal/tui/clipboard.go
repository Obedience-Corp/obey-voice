package tui

import (
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

// clipboardBackend keeps operating-system integration outside the editor
// model. Tests can provide an in-memory implementation through deps.
type clipboardBackend interface {
	ReadAll() (string, error)
	WriteAll(string) error
}

// The system calls are package variables so the test binary can replace them
// with a guard that refuses to touch the developer's real clipboard.
var (
	systemClipboardRead  = clipboard.ReadAll
	systemClipboardWrite = clipboard.WriteAll
)

type systemClipboard struct{}

func (systemClipboard) ReadAll() (string, error) {
	return systemClipboardRead()
}

func (systemClipboard) WriteAll(text string) error {
	return systemClipboardWrite(text)
}

func orSystemClipboard(b clipboardBackend) clipboardBackend {
	if b != nil {
		return b
	}
	return systemClipboard{}
}

type clipboardPasteMsg struct {
	text  string
	image []byte
	err   error
}

func readClipboard(backend clipboardBackend) tea.Cmd {
	return func() tea.Msg {
		if images, ok := backend.(interface{ ReadImage() ([]byte, error) }); ok {
			data, err := images.ReadImage()
			if err != nil || len(data) > 0 {
				return clipboardPasteMsg{image: data, err: err}
			}
		}
		text, err := backend.ReadAll()
		return clipboardPasteMsg{text: text, err: err}
	}
}

func (m conversationModel) clipboard() clipboardBackend {
	return orSystemClipboard(m.deps.clipboard)
}
