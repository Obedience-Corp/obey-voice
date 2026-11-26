package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// itemLayout is a transcript item drawn for one set of render inputs. An
// entry whose key no longer matches its item is redrawn, so an item edited in
// place never shows stale text.
type itemLayout struct {
	key  itemLayoutKey
	text string
	wrap wrapPiece
	// Pictures can animate and finish loading later, so they are drawn on
	// every refresh and only their wrapping is reused.
	media      string
	mediaWidth int
	mediaWrap  *wrapPiece
	part       string
}

// itemLayoutKey is everything chatItem.render reads.
type itemLayoutKey struct {
	kind       chatItemKind
	sys        string
	hang       string
	hangStyle  *lipgloss.Style
	name       string
	hosted     bool
	think      string
	thinkLabel bool
	spoken     string
	showThink  bool
	width      int
	profile    termenv.Profile
}

// layoutItem returns item i's layout, drawing it only when its inputs changed.
// The entry is stored on the item, so the slice element is updated in place.
func (m *conversationModel) layoutItem(i, width int, profile termenv.Profile) *itemLayout {
	it := &m.items[i]
	key := itemLayoutKey{
		kind: it.kind, sys: it.sys, hang: it.hang, hangStyle: it.hangStyle, name: it.name,
		hosted: it.hosted, think: it.think, thinkLabel: it.thinkLabel, spoken: it.spoken,
		showThink: m.showThink, width: width, profile: profile,
	}
	if l := it.layout; l != nil && l.key == key {
		return l
	}
	l := &itemLayout{key: key, text: it.render(m.showThink, width)}
	if l.text != "" {
		l.wrap = newWrapPiece(l.text, width)
	}
	it.layout = l
	return l
}

// withMedia returns the item's transcript part, its text with the pictures
// below, and the wrapped pictures.
func (l *itemLayout) withMedia(media string, width int) (string, *wrapPiece) {
	if l.mediaWrap == nil || media != l.media || width != l.mediaWidth {
		p := newWrapPiece(media, width)
		l.media, l.mediaWidth, l.mediaWrap = media, width, &p
		l.part = l.text + "\n" + media
	}
	return l.part, l.mediaWrap
}

// wrapPiece is part of the transcript wrapped on its own the way the whole
// transcript is wrapped. Every row is padded to width columns; inside the
// transcript rows are padded further, to the widest row of all.
type wrapPiece struct {
	wrapped string
	width   int
	rows    int
	// joinable is false when the piece would not read the same inside the
	// joined transcript: a style or link left open at its end carries over
	// the next line break, and a trailing carriage return merges with it.
	joinable bool
	padded   string
	paddedTo int
}

// joinProbe follows a piece while it is wrapped; a style or link the piece
// leaves open shows up on the probe's row.
const joinProbe = "."

func newWrapPiece(s string, width int) wrapPiece {
	out := lipgloss.NewStyle().Width(width).Render(s + "\n" + joinProbe)
	cut := strings.LastIndexByte(out, '\n')
	probe := out[cut+1:]
	return wrapPiece{
		wrapped:  out[:cut],
		width:    ansi.StringWidth(probe),
		rows:     strings.Count(out[:cut], "\n") + 1,
		joinable: strings.TrimRight(probe, " ") == joinProbe && !strings.HasSuffix(s, "\r"),
	}
}

// padTo returns the piece with every row padded to width columns.
func (p *wrapPiece) padTo(width int) string {
	if width <= p.width {
		return p.wrapped
	}
	if p.paddedTo != width {
		pad := strings.Repeat(" ", width-p.width)
		p.padded = strings.ReplaceAll(p.wrapped, "\n", pad+"\n") + pad
		p.paddedTo = width
	}
	return p.padded
}

// transcriptView is the wrapped transcript the viewport holds.
type transcriptView struct {
	content string
	// When content was joined from pieces: the padded pieces, their join and
	// the wrapped live bubble.
	joined   bool
	pieces   []string
	finished string
	live     string
}

// showTranscript puts the transcript in the viewport. Finished items reuse
// their wrapped pieces, so a streamed delta wraps only the live bubble. When
// the pieces would not join unchanged it wraps everything at once.
func (m *conversationModel) showTranscript(parts []string, pieces []*wrapPiece, live string) {
	width := m.viewport.Width
	wrap := lipgloss.NewStyle().Width(width)
	joinable := len(pieces) > 0
	widest := width
	for _, p := range pieces {
		joinable = joinable && p.joinable
		widest = max(widest, p.width)
	}
	if !joinable {
		content := strings.Join(parts, "\n")
		if live != "" {
			if content != "" {
				content += "\n" + live
			} else {
				content = live
			}
		}
		m.setTranscriptView(transcriptView{content: wrap.Render(content)})
		return
	}
	liveWrapped, liveWidth := "", 0
	if live != "" {
		liveWrapped = wrap.Render(live)
		liveWidth = lipgloss.Width(liveWrapped)
		widest = max(widest, liveWidth)
	}
	padded := make([]string, len(pieces))
	for i, p := range pieces {
		padded[i] = p.padTo(widest)
	}
	if liveWidth > 0 && liveWidth < widest {
		pad := strings.Repeat(" ", widest-liveWidth)
		liveWrapped = strings.ReplaceAll(liveWrapped, "\n", pad+"\n") + pad
	}
	prev := m.shown
	reused := prev.joined && sameStrings(prev.pieces, padded)
	if reused && liveWrapped == prev.live && m.viewport.YOffset < m.viewport.TotalLineCount() {
		return
	}
	finished := prev.finished
	if !reused {
		finished = strings.Join(padded, "\n")
	}
	content := finished
	if liveWrapped != "" {
		content += "\n" + liveWrapped
	}
	m.setTranscriptView(transcriptView{content: content, joined: true, pieces: padded, finished: finished, live: liveWrapped})
}

func (m *conversationModel) setTranscriptView(v transcriptView) {
	m.shown = v
	m.viewport.SetContent(v.content)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
