package tui

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func testGraphics(cellW, cellH int) (*mediaGraphics, *lockedBuffer) {
	out := &lockedBuffer{}
	g := newMediaGraphics(out, func() (int, int) { return cellW, cellH })
	g.measure()
	return g, out
}

// Runs a command tree to completion and applies every media message.
func drainMedia(t *testing.T, m *conversationModel, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			drainMedia(t, m, c)
		}
	case nil:
	default:
		m.updateMedia(msg)
	}
}

var kittyCommand = regexp.MustCompile(`\x1b_G([^;\x1b]*)(?:;([^\x1b]*))?\x1b\\`)

// Reassembles the PNG the terminal would receive, with the first chunk's keys.
func sentImage(t *testing.T, stream string) (string, []byte) {
	t.Helper()
	var keys, payload string
	for i, c := range kittyCommand.FindAllStringSubmatch(stream, -1) {
		if i == 0 {
			keys = c[1]
		}
		payload += c[2]
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	return keys, data
}

func TestGraphicsBoxKeepsShapeAndNeverEnlarges(t *testing.T) {
	g, _ := testGraphics(17, 34)
	screenshot := chatmedia.Ref{ID: "s", Width: 1474, Height: 2012}
	box := g.box(screenshot, 120, 24)
	// 24 rows are 816 pixels, so the width is 1474*816/2012 = 597 pixels = 36 cells.
	if box.cols != 36 || box.rows != 24 {
		t.Fatalf("portrait screenshot box = %dx%d, want 36x24", box.cols, box.rows)
	}
	if box := g.box(screenshot, 20, 24); box.cols != 20 || box.rows != 14 {
		t.Fatalf("width-limited box = %dx%d, want 20x14", box.cols, box.rows)
	}
	if box := g.box(chatmedia.Ref{ID: "i", Width: 40, Height: 40}, 120, 24); box.cols != 3 || box.rows != 2 {
		t.Fatalf("small icon box = %dx%d, want 3x2", box.cols, box.rows)
	}
	if box := g.box(chatmedia.Ref{ID: "g", Width: 400, Height: 400, Frames: 8}, 120, 24); box.id != "" {
		t.Fatal("an animated GIF got a still-image box")
	}
	var none *mediaGraphics
	if box := none.box(screenshot, 120, 24); box.id != "" {
		t.Fatal("no graphics still produced a box")
	}
}

func TestPlaceholderRowsSurviveTheTextPipeline(t *testing.T) {
	const cols, rows = 12, 5
	text := mediaPlaceholders(0x0a0b0c, cols, rows)
	lines := strings.Split(text, "\n")
	if len(lines) != rows {
		t.Fatalf("rows = %d, want %d", len(lines), rows)
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w != cols {
			t.Fatalf("row width = %d, want %d", w, cols)
		}
		if !strings.HasPrefix(line, "\x1b[38;2;10;11;12m") {
			t.Fatalf("row does not carry the image id as its colour: %q", line)
		}
		if got := ansi.Truncate(line, cols, ""); got != line {
			t.Fatal("truncating at the row's own width changed it")
		}
	}
	// A draft card pads its preview; the cells must come through whole.
	card := lipgloss.NewStyle().Width(cols + 2).Render(text)
	for _, line := range lines {
		if !strings.Contains(card, line) {
			t.Fatalf("lipgloss broke a placeholder row:\n%q", card)
		}
	}
	vp := viewport.New(cols+4, 3)
	vp.SetContent("before\n" + text + "\nafter")
	vp.SetYOffset(2)
	view := vp.View()
	for _, line := range lines[1:4] {
		if !strings.Contains(view, line) {
			t.Fatalf("scrolled viewport lost a placeholder row:\n%q", view)
		}
	}
	if strings.Contains(view, lines[0]) || strings.Contains(view, lines[4]) {
		t.Fatal("viewport showed a row outside its window")
	}
}

type placeholderProgram struct{ text string }

func (p placeholderProgram) Init() tea.Cmd {
	return tea.Sequence(func() tea.Msg { return tea.WindowSizeMsg{Width: 30, Height: 10} }, tea.Quit)
}
func (p placeholderProgram) Update(tea.Msg) (tea.Model, tea.Cmd) { return p, nil }
func (p placeholderProgram) View() string                        { return p.text }

func TestPlaceholderRowsSurviveTheRenderer(t *testing.T) {
	text := mediaPlaceholders(77, 8, 3)
	var out bytes.Buffer
	p := tea.NewProgram(placeholderProgram{text}, tea.WithInput(nil), tea.WithOutput(&out), tea.WithoutSignalHandler())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("renderer output lost a placeholder row: %q", out.String())
		}
	}
}

func TestGraphicsSyncSendsOnceResizesAndFrees(t *testing.T) {
	m, _ := mediaTestModel(t)
	ref := m.media.draft[0]
	g, out := testGraphics(4, 8)
	store := chatmedia.DefaultStore()
	box := g.box(ref, 40, 10)
	if box.id == "" {
		t.Fatalf("fixture %dx%d got no box", ref.Width, ref.Height)
	}
	placed, ok := g.sync(g.nextSync(), []mediaWant{{box, ref}}, store)
	if !ok || placed[box] == 0 {
		t.Fatalf("image was not placed: %v", placed)
	}
	keys, data := sentImage(t, out.String())
	for _, want := range []string{"a=T", "U=1", "q=2", "f=100", "i=" + itoa(placed[box]), "c=" + itoa(box.cols), "r=" + itoa(box.rows)} {
		if !strings.Contains(","+keys+",", ","+want+",") {
			t.Fatalf("transmission keys %q lack %s", keys, want)
		}
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("transmitted payload is not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() > box.cols*4 || b.Dy() > box.rows*8 || b.Dx() < 2 || b.Dy() < 2 {
		t.Fatalf("transmitted %dx%d pixels for a %dx%d cell box", b.Dx(), b.Dy(), box.cols, box.rows)
	}

	sent := len(out.String())
	again, _ := g.sync(g.nextSync(), []mediaWant{{box, ref}}, store)
	if len(out.String()) != sent || again[box] != placed[box] {
		t.Fatal("an image the terminal already holds was sent again")
	}

	stale := g.nextSync()
	smaller := g.box(ref, 40, 4)
	if smaller == box {
		t.Fatal("fixture too small to exercise a resize")
	}
	resized, _ := g.sync(g.nextSync(), []mediaWant{{smaller, ref}}, store)
	if resized[smaller] == 0 || resized[box] != 0 {
		t.Fatalf("resize kept the old box: %v", resized)
	}
	if !strings.Contains(out.String()[sent:], "a=d,d=I,i="+itoa(placed[box])+",") {
		t.Fatal("the replaced image was not freed")
	}
	if _, ok := g.sync(stale, []mediaWant{{box, ref}}, store); ok {
		t.Fatal("an older request overwrote a newer one")
	}

	sent = len(out.String())
	g.close()
	if !strings.Contains(out.String()[sent:], "a=d,d=I,i="+itoa(resized[smaller])+",") {
		t.Fatal("exit left an image in the terminal")
	}
}

func TestGraphicsSyncKeepsHalfBlocksForAnUnreadableImage(t *testing.T) {
	m, _ := mediaTestModel(t)
	g, out := testGraphics(4, 8)
	missing := chatmedia.Ref{ID: "gone", Name: "gone.png", Format: "png", Width: 64, Height: 64}
	box := g.box(missing, 40, 10)
	placed, ok := g.sync(g.nextSync(), []mediaWant{{box, missing}}, chatmedia.DefaultStore())
	if !ok || len(placed) != 0 || out.String() != "" {
		t.Fatalf("unreadable image was placed: %v %q", placed, out.String())
	}
	m.graphics = g
	m.media.placed = placed
	if _, native := m.nativeImage(missing, 40, 10); native {
		t.Fatal("unreadable image rendered as native")
	}
}

func TestConversationShowsNativeStillsAndHalfBlockGIFs(t *testing.T) {
	m, _ := mediaTestModel(t)
	g, out := testGraphics(4, 8)
	m.adoptGraphics(g)
	still := m.media.draft[0]

	if strings.Contains(m.draftMediaView(), string(rune(0x10EEEE))) {
		t.Fatal("placeholders shown before the terminal holds the image")
	}
	drainMedia(t, &m, m.mediaCommand())
	draft := m.draftMediaView()
	if !strings.Contains(draft, string(rune(0x10EEEE))) || strings.Contains(draft, "▀") {
		t.Fatalf("draft still image is not native:\n%q", draft)
	}

	cmd := m.importMedia(nil, testmedia.GIF())
	m.updateMedia(cmd())
	animated := m.media.draft[1]
	m.media.paused = true
	m.items = append(m.items, chatItem{kind: chatItemSys, sys: "sent", attachments: []chatmedia.Ref{still, animated}})
	m.media.draft = nil
	m.refreshContent()
	drainMedia(t, &m, m.mediaCommand())

	thread := strings.Join(m.transcript, "\n")
	width, rows := max(1, m.viewport.Width-4), m.threadPreviewRows(2)
	native, ok := m.nativeImage(still, width, rows)
	if !ok || !strings.Contains(thread, native) {
		t.Fatalf("thread still image is not native:\n%q", thread)
	}
	if _, ok := m.nativeImage(animated, width, rows); ok || !strings.Contains(thread, "▀") {
		t.Fatal("animated GIF left half-blocks")
	}
	if n := len(kittyCommand.FindAllString(out.String(), -1)); n == 0 {
		t.Fatal("nothing was transmitted")
	}

	// A new conversation must free what the old one showed.
	sent := len(out.String())
	m.clearTranscript()
	drainMedia(t, &m, m.mediaCommand())
	if !strings.Contains(out.String()[sent:], "a=d,d=I") || len(m.media.placed) != 0 {
		t.Fatalf("cleared conversation left images in the terminal: %q", out.String()[sent:])
	}
}

func TestConversationWithoutGraphicsKeepsHalfBlocks(t *testing.T) {
	m, _ := mediaTestModel(t)
	drainMedia(t, &m, m.mediaCommand())
	draft := m.draftMediaView()
	if strings.Contains(draft, string(rune(0x10EEEE))) || !strings.Contains(draft, "▀") {
		t.Fatalf("draft without graphics is not half-blocks:\n%q", draft)
	}
	if m.threadPreviewRows(1) != max(10, min(24, m.viewport.Height/2)) {
		t.Fatal("half-block row budget changed")
	}
}

func TestMediaGraphicsDetection(t *testing.T) {
	for name, c := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"ghostty":            {map[string]string{"TERM": "xterm-ghostty"}, true},
		"kitty":              {map[string]string{"TERM": "xterm-kitty"}, true},
		"kitty over ssh":     {map[string]string{"TERM": "xterm-256color", "KITTY_WINDOW_ID": "3"}, true},
		"plain terminal":     {map[string]string{"TERM": "xterm-256color"}, false},
		"ghostty under tmux": {map[string]string{"TERM": "xterm-ghostty", "TMUX": "/tmp/t"}, false},
		"ghostty, blocks":    {map[string]string{"TERM": "xterm-ghostty", "OBEY_VOICE_TUI_IMAGES": "blocks"}, false},
		"forced under tmux":  {map[string]string{"TERM": "tmux-256color", "TMUX": "/tmp/t", "OBEY_VOICE_TUI_IMAGES": "kitty"}, true},
	} {
		env := func(k string) string { return c.env[k] }
		if got := wantsMediaGraphics(env); got != c.want {
			t.Errorf("%s: wantsMediaGraphics = %v, want %v", name, got, c.want)
		}
		// A nil file stands for output that is not a terminal.
		if detectMediaGraphics(env, nil) != nil {
			t.Errorf("%s: graphics enabled without a terminal", name)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
