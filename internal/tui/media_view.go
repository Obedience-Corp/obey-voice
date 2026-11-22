package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Half-block previews survive redraw, scrolling and tmux without a terminal
// graphics extension; mediaGraphics replaces them where the terminal has one.
// F3 opens the original at full resolution in its viewer.
func mediaPixels(img image.Image, width, height int) string {
	if img == nil {
		return "Loading preview…"
	}
	img = chatmedia.Thumbnail(img, max(1, width), max(1, height)*2)
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
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
		blocks = append(blocks, dimStyle.Render(ansi.Truncate(mediaSummary(r), width, "…"))+"\n"+m.mediaBody(r, width, height))
	}
	return strings.Join(blocks, "\n")
}

func (m conversationModel) mediaBody(r chatmedia.Ref, width, height int) string {
	preview, exists := m.media.cache[r.ID]
	switch {
	case !exists:
		return "Preview outside cache · use /image N to open original"
	case preview.err != nil:
		return errorStyle.Render("Image unavailable: " + preview.err.Error())
	}
	if native, ok := m.nativeImage(r, max(1, width), height); ok {
		return native
	}
	return mediaPixels(preview.preview.Frame(time.Since(m.media.started), m.media.paused || m.reducedMotion), max(1, width), height)
}
func (m conversationModel) draftMediaView() string {
	if len(m.media.draft) == 0 {
		return ""
	}
	errorView := m.draftErrorView()
	var cards []string
	w, h := m.draftPreviewSize()
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
func (m conversationModel) draftErrorView() string {
	if m.media.err == "" {
		return ""
	}
	text := strings.Join(strings.Fields(safeWorkerText(m.media.err)), " ")
	text = ansi.Truncate(text, max(1, m.width)*3, "…")
	return errorStyle.Render(ansi.Hardwrap(text, max(1, m.width), false))
}

func (m conversationModel) draftPreviewSize() (int, int) {
	inputHeight := conversationInputHeight
	if m.height < 12 {
		inputHeight = 1
	}
	// Use available space for recognizable thumbnails; four rows reduced a
	// portrait screenshot to only a handful of colored cells.
	w := max(1, min(64, m.width/max(1, len(m.media.draft))-2))
	// Leave room for chrome, labels, the editor, the error and a transcript row.
	errorRows := 0
	if errorView := m.draftErrorView(); errorView != "" {
		errorRows = lipgloss.Height(errorView)
	}
	h := max(1, min(12, m.height/4, m.height-inputHeight-10-errorRows-m.workStripReservedRows))
	return w, h
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
	return m.openPictureCmd(chatmedia.DefaultStore().Path(refs[len(refs)-1]))
}

type mediaOpenError struct{ err error }
