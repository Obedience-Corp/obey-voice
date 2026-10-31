package tui

import (
	"fmt"
	"image"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Half-block previews survive redraw, scrolling and tmux without a terminal
// graphics extension. F3 opens the original at full resolution in its viewer.
func mediaPixels(img image.Image, width, height int) string {
	if img == nil {
		return "Loading preview…"
	}
	b := img.Bounds()
	w := max(1, min(width, b.Dx()))
	h := max(1, min(height*2, b.Dy()*w/b.Dx()))
	w = max(1, min(w, b.Dx()*h/max(1, b.Dy())))
	var out strings.Builder
	for y := 0; y < h; y += 2 {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h).RGBA()
			r2, g2, b2, _ := img.At(b.Min.X+x*b.Dx()/w, b.Min.Y+min(y+1, h-1)*b.Dy()/h).RGBA()
			fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", r>>8, g>>8, bl>>8, r2>>8, g2>>8, b2>>8)
		}
		out.WriteString("\x1b[0m")
		if y+2 < h {
			out.WriteByte('\n')
		}
	}
	return out.String()
}
func (m conversationModel) renderMedia(refs []chatmedia.Ref, width, height int) string {
	var blocks []string
	for _, r := range refs {
		preview, exists := m.media.cache[r.ID]
		body := ""
		if !exists {
			body = "Preview outside cache · use /image N to open original"
		} else if preview.err != nil {
			body = errorStyle.Render("Image unavailable: " + preview.err.Error())
		} else {
			body = mediaPixels(preview.preview.Frame(time.Since(m.media.started), m.media.paused || m.reducedMotion), max(1, width), height)
		}
		blocks = append(blocks, dimStyle.Render(ansi.Truncate(mediaSummary(r), width, "…"))+"\n"+body)
	}
	return strings.Join(blocks, "\n")
}
func (m conversationModel) draftMediaView() string {
	if len(m.media.draft) == 0 {
		return ""
	}
	var errorView string
	if m.media.err != "" {
		text := strings.Join(strings.Fields(safeWorkerText(m.media.err)), " ")
		text = ansi.Truncate(text, max(1, m.width)*3, "…")
		errorView = errorStyle.Render(ansi.Hardwrap(text, max(1, m.width), false))
	}
	inputHeight := conversationInputHeight
	if m.height < 12 {
		inputHeight = 1
	}
	errorRows := 0
	if errorView != "" {
		errorRows = lipgloss.Height(errorView)
	}
	var cards []string
	// Use available space for recognizable thumbnails; four rows reduced a
	// portrait screenshot to only a handful of colored cells.
	w := max(1, min(64, m.width/len(m.media.draft)-2))
	// Leave room for chrome, labels, the editor, the error and a transcript row.
	h := max(1, min(12, m.height/4, m.height-inputHeight-10-errorRows-m.workStripReservedRows))
	for _, r := range m.media.draft {
		cards = append(cards, lipgloss.NewStyle().Width(w+2).Render(m.renderMedia([]chatmedia.Ref{r}, w, h)))
	}
	view := lipgloss.JoinHorizontal(lipgloss.Top, cards...)
	hint := "F2 pause GIFs · F3 open original · F4 remove last image"
	if m.media.paused {
		hint = "F2 play GIFs · F3 open original · F4 remove last image"
	}
	view += "\n" + dimStyle.Render(ansi.Truncate(hint, max(1, m.width), "…"))
	if errorView != "" {
		view += "\n" + errorView
	}
	return view
}
func (m conversationModel) draftMediaHeight() int {
	v := m.draftMediaView()
	if v == "" {
		return 0
	}
	return lipgloss.Height(v) + 1
}
func (m *conversationModel) openMedia() tea.Cmd {
	var refs []chatmedia.Ref
	if len(m.media.draft) > 0 {
		refs = m.media.draft
	} else {
		for _, item := range m.items {
			if len(item.attachments) > 0 {
				refs = item.attachments
			}
		}
	}
	if len(refs) == 0 {
		m.commandError("No image to open")
		return nil
	}
	return openMediaFile(chatmedia.DefaultStore().Path(refs[len(refs)-1]))
}
func openMediaFile(path string) tea.Cmd {
	return func() tea.Msg {
		command := "xdg-open"
		if runtime.GOOS == "darwin" {
			command = "open"
		}
		if runtime.GOOS == "windows" {
			return mediaOpenError{fmt.Errorf("open the attachment through your desktop file viewer")}
		}
		return mediaOpenError{exec.Command(command, path).Run()}
	}
}

type mediaOpenError struct{ err error }
