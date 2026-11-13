package tui

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/charmbracelet/x/term"
)

// Row and column numbers are spelled with one diacritic each, and the table
// has 297 of them.
const maxPlaceholderSpan = 297

// mediaGraphics shows still images at the terminal's own resolution through
// the Kitty graphics protocol. Each image is sent once and then placed with
// placeholder cells, which are ordinary text: they scroll, clip and redraw
// with the transcript. Terminals without the protocol keep half-blocks.
type mediaGraphics struct {
	out          io.Writer
	cellSize     func() (int, int)
	cellW, cellH atomic.Int64
	issued       atomic.Uint64

	mu      sync.Mutex
	applied uint64
	next    int
	sent    map[mediaBox]int
}

// mediaBox is one image at one size in cells; a resize makes a new one.
type mediaBox struct {
	id         string
	cols, rows int
}

type mediaWant struct {
	box mediaBox
	ref chatmedia.Ref
}

type mediaPlaced struct {
	token  *int
	placed map[mediaBox]int
}

// The choice is made from the environment because a capability query's reply
// would arrive on the input Bubble Tea is reading. Multiplexers are left on
// half-blocks: they need passthrough that is off by default.
func detectMediaGraphics(env func(string) string, out *os.File) *mediaGraphics {
	if !wantsMediaGraphics(env) || out == nil || !term.IsTerminal(out.Fd()) {
		return nil
	}
	g := newMediaGraphics(out, func() (int, int) { return terminalCellPixels(out) })
	g.measure()
	return g
}

func wantsMediaGraphics(env func(string) string) bool {
	switch strings.ToLower(env("OBEY_VOICE_TUI_IMAGES")) {
	case "blocks":
		return false
	case "kitty":
		return true
	}
	if env("TMUX") != "" || env("ZELLIJ") != "" || env("STY") != "" {
		return false
	}
	name := env("TERM")
	return strings.Contains(name, "kitty") || strings.Contains(name, "ghostty") ||
		env("KITTY_WINDOW_ID") != "" || env("GHOSTTY_RESOURCES_DIR") != ""
}

func newMediaGraphics(out io.Writer, cellSize func() (int, int)) *mediaGraphics {
	return &mediaGraphics{out: out, cellSize: cellSize, next: 1 + rand.IntN(1<<23), sent: make(map[mediaBox]int)}
}

func (g *mediaGraphics) measure() {
	if g == nil {
		return
	}
	w, h := 0, 0
	if g.cellSize != nil {
		w, h = g.cellSize()
	}
	if w <= 0 || h <= 0 {
		// The terminal did not report pixels; a common cell is close enough
		// to keep the picture's shape.
		w, h = 8, 16
	}
	g.cellW.Store(int64(w))
	g.cellH.Store(int64(h))
}

// box fits the image inside width×height cells without enlarging it past
// one image pixel per screen pixel.
func (g *mediaGraphics) box(r chatmedia.Ref, width, height int) mediaBox {
	if g == nil || r.Frames > 1 || r.Width <= 0 || r.Height <= 0 || width <= 0 || height <= 0 {
		return mediaBox{}
	}
	cw, ch := int(g.cellW.Load()), int(g.cellH.Load())
	if cw <= 0 || ch <= 0 {
		return mediaBox{}
	}
	width, height = min(width, maxPlaceholderSpan), min(height, maxPlaceholderSpan)
	pw, ph := r.Width, r.Height
	if limit := width * cw; pw > limit {
		ph = max(1, ph*limit/pw)
		pw = limit
	}
	if limit := height * ch; ph > limit {
		pw = max(1, pw*limit/ph)
		ph = limit
	}
	return mediaBox{id: r.ID, cols: min(width, (pw+cw-1)/cw), rows: min(height, (ph+ch-1)/ch)}
}

func (g *mediaGraphics) nextSync() uint64 { return g.issued.Add(1) }

// sync makes the terminal hold exactly the wanted images and reports what it
// holds. An older request that runs after a newer one does nothing.
func (g *mediaGraphics) sync(seq uint64, want []mediaWant, store chatmedia.Store) (map[mediaBox]int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if seq < g.applied {
		return nil, false
	}
	g.applied = seq
	wanted := make(map[mediaBox]bool, len(want))
	for _, w := range want {
		wanted[w.box] = true
	}
	var out bytes.Buffer
	for box, id := range g.sent {
		if !wanted[box] {
			writeImageDelete(&out, id)
			delete(g.sent, box)
		}
	}
	cw, ch := int(g.cellW.Load()), int(g.cellH.Load())
	for _, w := range want {
		if _, ok := g.sent[w.box]; ok {
			continue
		}
		// Zero records a failure, so the image stays on half-blocks instead
		// of being decoded again on every event.
		g.sent[w.box] = 0
		img, err := loadStill(store, w.ref)
		if err != nil {
			continue
		}
		id := g.next
		g.next = g.next%(1<<24-1) + 1
		var one bytes.Buffer
		err = kitty.EncodeGraphics(&one, chatmedia.Thumbnail(img, w.box.cols*cw, w.box.rows*ch), &kitty.Options{
			Action: kitty.TransmitAndPut, Transmission: kitty.Direct, Format: kitty.PNG, Chunk: true, Quite: 2,
			ID: id, VirtualPlacement: true, Columns: w.box.cols, Rows: w.box.rows,
		})
		if err != nil {
			continue
		}
		out.Write(one.Bytes())
		g.sent[w.box] = id
	}
	// One write, so a frame Bubble Tea draws at the same moment cannot land
	// in the middle of an image.
	if out.Len() > 0 {
		if _, err := g.out.Write(out.Bytes()); err != nil {
			clear(g.sent)
		}
	}
	placed := make(map[mediaBox]int, len(g.sent))
	for box, id := range g.sent {
		if id != 0 {
			placed[box] = id
		}
	}
	return placed, true
}

// close frees what the terminal still holds; image data outlives the program
// otherwise.
func (g *mediaGraphics) close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.applied = g.issued.Add(1)
	var out bytes.Buffer
	for box, id := range g.sent {
		writeImageDelete(&out, id)
		delete(g.sent, box)
	}
	if out.Len() > 0 {
		_, _ = g.out.Write(out.Bytes())
	}
}

func writeImageDelete(w io.Writer, id int) {
	if id != 0 {
		fmt.Fprintf(w, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
	}
}

func loadStill(store chatmedia.Store, r chatmedia.Ref) (image.Image, error) {
	data, err := store.Read(r)
	if err != nil {
		return nil, err
	}
	if _, err = chatmedia.Inspect(r.Name, data); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// The image id travels as the cell's foreground colour.
func mediaPlaceholders(id, cols, rows int) string {
	var out strings.Builder
	for row := range rows {
		if row > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
		for col := range cols {
			out.WriteRune(kitty.Placeholder)
			out.WriteRune(kitty.Diacritic(row))
			out.WriteRune(kitty.Diacritic(col))
		}
		out.WriteString("\x1b[39m")
	}
	return out.String()
}

// A conversation that starts with images still in the terminal must not
// treat "none wanted" as already in effect, or they would never be freed.
func (m *conversationModel) adoptGraphics(g *mediaGraphics) {
	m.graphics = g
	if g != nil {
		m.media.wantSignature = "stale"
	}
}

func (m conversationModel) nativeImage(r chatmedia.Ref, width, height int) (string, bool) {
	box := m.graphics.box(r, width, height)
	id := m.media.placed[box]
	if id == 0 {
		return "", false
	}
	return mediaPlaceholders(id, box.cols, box.rows), true
}

func (m conversationModel) threadPreviewRows(attachments int) int {
	if m.graphics != nil {
		// Real pixels reward height: a screenshot is readable at most of the pane.
		return max(10, min(32, m.viewport.Height*3/4/attachments))
	}
	return max(10, min(24, m.viewport.Height/(2*attachments)))
}

// syncGraphics asks for the images the screen shows now and nothing else.
func (m *conversationModel) syncGraphics(keep map[string]bool) tea.Cmd {
	g := m.graphics
	if g == nil {
		return nil
	}
	var want []mediaWant
	seen := make(map[mediaBox]bool)
	add := func(r chatmedia.Ref, width, height int) {
		box := g.box(r, width, height)
		if box.id == "" || !keep[r.ID] || seen[box] {
			return
		}
		seen[box] = true
		want = append(want, mediaWant{box, r})
	}
	for _, item := range m.items {
		for _, r := range item.attachments {
			add(r, max(1, m.viewport.Width-4), m.threadPreviewRows(len(item.attachments)))
		}
	}
	if len(m.media.draft) > 0 {
		w, h := m.draftPreviewSize()
		for _, r := range m.media.draft {
			add(r, w, h)
		}
	}
	keys := make([]string, 0, len(want))
	for _, w := range want {
		keys = append(keys, fmt.Sprintf("%s/%d/%d", w.box.id, w.box.cols, w.box.rows))
	}
	sort.Strings(keys)
	signature := strings.Join(keys, "\n")
	if signature == m.media.wantSignature {
		return nil
	}
	m.media.wantSignature = signature
	token, seq, store := m.media.token, g.nextSync(), chatmedia.DefaultStore()
	return func() tea.Msg {
		placed, ok := g.sync(seq, want, store)
		if !ok {
			return nil
		}
		return mediaPlaced{token, placed}
	}
}
