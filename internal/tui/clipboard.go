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
	// returned counts the refused drafts that had come back to the input
	// when the paste was asked for.
	returned int
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

// pasteClipboard reads the clipboard for the input as it is now. A refused
// draft that comes back before the read ends is not what the user pasted into.
func (m *conversationModel) pasteClipboard() tea.Cmd {
	read, returned := readClipboard(m.clipboard()), m.media.returned
	return func() tea.Msg {
		msg := read()
		if paste, ok := msg.(clipboardPasteMsg); ok {
			paste.returned = returned
			return paste
		}
		return msg
	}
}

func (m conversationModel) clipboard() clipboardBackend {
	return orSystemClipboard(m.deps.clipboard)
}
