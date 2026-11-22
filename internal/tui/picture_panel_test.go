package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type copiedPicture struct{ path, format string }

type pictureClipboard struct {
	text     string
	pictures []copiedPicture
	err      error
}

func (c *pictureClipboard) ReadAll() (string, error) { return c.text, nil }
func (c *pictureClipboard) WriteAll(text string) error {
	c.text = text
	return c.err
}
func (c *pictureClipboard) WriteImage(_ context.Context, path, format string) error {
	c.pictures = append(c.pictures, copiedPicture{path, format})
	return c.err
}

type pictureRig struct {
	clip   *pictureClipboard
	opened []string
	runner *imageTurnRunner
}

func storePicture(t *testing.T, name string, data []byte) chatmedia.Ref {
	t.Helper()
	r, err := chatmedia.DefaultStore().Import(name, data)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// harborFixture is large enough that the panel shows it bigger than the
// transcript does.
func harborFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 480, 288))
	for y := range 288 {
		for x := range 480 {
			img.SetRGBA(x, y, color.RGBA{20, uint8(35 + y/3), uint8(65 + x/4), 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func jpegFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 90, 60))
	for y := range 60 {
		for x := range 90 {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 2), 120, uint8(y * 3), 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// pictureConversation builds a conversation whose entries come from build,
// with every picture loaded and, when g is set, held by the terminal.
func pictureConversation(t *testing.T, width, height int, g *mediaGraphics, build func(harbor chatmedia.Ref) []chatItem) (conversationModel, *pictureRig, chatmedia.Ref) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	rig := &pictureRig{clip: &pictureClipboard{}, runner: &imageTurnRunner{}}
	m := sizedConversation(t, width, height)
	m.deps = conversationDeps{runner: rig.runner, ctx: t.Context(), clipboard: rig.clip}
	m.cfg = &config.Config{BrainProvider: "ollama"}
	m.openPicture = func(_ context.Context, path string) error {
		rig.opened = append(rig.opened, path)
		return nil
	}
	if g != nil {
		m.adoptGraphics(g)
	}
	harbor := storePicture(t, "harbor.png", harborFixture(t))
	m.items = append(m.items, build(harbor)...)
	m.refreshContent()
	drainMedia(t, &m, m.mediaCommand())
	m.viewport.GotoBottom()
	return m, rig, harbor
}

func sys(text string, refs ...chatmedia.Ref) chatItem {
	return chatItem{kind: chatItemSys, sys: text, attachments: refs}
}

func spacers(n int) []chatItem {
	var out []chatItem
	for i := range n {
		out = append(out, sys(fmt.Sprintf("spacer %d", i)))
	}
	return out
}

// screenCell finds the nth occurrence of text on the rendered screen.
func screenCell(t *testing.T, m conversationModel, text string, nth int) (int, int) {
	t.Helper()
	for y, line := range strings.Split(m.View(), "\n") {
		plain := ansi.Strip(line)
		for offset := 0; ; {
			i := strings.Index(plain[offset:], text)
			if i < 0 {
				break
			}
			if nth == 0 {
				return ansi.StringWidth(plain[:offset+i]), y
			}
			nth--
			offset += i + len(text)
		}
	}
	t.Fatalf("missing %q on screen:\n%s", text, ansi.Strip(m.View()))
	return 0, 0
}

func click(t *testing.T, m conversationModel, x, y int) (conversationModel, tea.Cmd) {
	t.Helper()
	return m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
}

func pressPanel(m conversationModel, keys ...tea.KeyMsg) conversationModel {
	for _, k := range keys {
		m, _ = m.Update(k)
	}
	return m
}

func panelKey(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "f3":
		return tea.KeyMsg{Type: tea.KeyF3}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

const placeholderCell = string(rune(0x10EEEE))

var renderers = map[string]func() *mediaGraphics{
	"half-blocks": func() *mediaGraphics { return nil },
	"kitty":       func() *mediaGraphics { g, _ := testGraphics(4, 8); return g },
}

func bodyCell(name string) string {
	if name == "kitty" {
		return placeholderCell
	}
	return "▀"
}

func TestPictureClickOpensPanelForThatPicture(t *testing.T) {
	for name, graphics := range renderers {
		t.Run(name, func(t *testing.T) {
			m, _, harbor := pictureConversation(t, 100, 40, graphics(), func(harbor chatmedia.Ref) []chatItem {
				return []chatItem{sys("Milo shared the harbor", harbor), sys("Vera replied")}
			})
			if name == "kitty" && !strings.Contains(m.View(), placeholderCell) {
				t.Fatal("transcript is not drawn with placeholder cells")
			}
			x, y := screenCell(t, m, "harbor.png · 480×288", 0)
			targets := map[string][2]int{
				"name line": {x + 3, y},
				"body":      {2, y + 2},
			}
			for target, at := range targets {
				opened, _ := click(t, m, at[0], at[1])
				if !opened.pictures.open || opened.pictures.spot != (pictureSpot{0, 0, harbor}) {
					t.Fatalf("%s click opened %+v", target, opened.pictures)
				}
			}
			for target, at := range map[string][2]int{
				"right of the name": {m.width - 2, y},
				"right of the body": {m.width - 2, y + 2},
				"text entry":        {3, y - 1},
				"header":            {1, 0},
			} {
				if got, _ := click(t, m, at[0], at[1]); got.pictures.open {
					t.Fatalf("click on %s opened the panel", target)
				}
			}
		})
	}
}

func TestPictureClickFollowsScrollWrappingAndRepeats(t *testing.T) {
	for name, graphics := range renderers {
		t.Run(name, func(t *testing.T) {
			long := strings.Repeat("a long line that wraps across the narrow screen ", 6)
			m, _, harbor := pictureConversation(t, 48, 30, graphics(), func(harbor chatmedia.Ref) []chatItem {
				items := []chatItem{sys(long, harbor)}
				items = append(items, spacers(30)...)
				return append(items, sys(long, harbor), sys("after"))
			})
			last := len(m.items) - 2

			m.viewport.GotoTop()
			x, y := screenCell(t, m, "harbor.png", 0)
			if spot, ok := m.pictureHit(x, y+1); !ok || spot != (pictureSpot{0, 0, harbor}) {
				t.Fatalf("top picture resolved to %+v %v", spot, ok)
			}
			m.viewport.GotoBottom()
			x, y = screenCell(t, m, "harbor.png", 0)
			if spot, ok := m.pictureHit(x, y+1); !ok || spot != (pictureSpot{last, 0, harbor}) {
				t.Fatalf("bottom picture resolved to %+v %v, want entry %d", spot, ok, last)
			}
			body := bodyCell(name)
			bx, by := screenCell(t, m, body, 0)
			got, _ := click(t, m, bx, by)
			if got.pictures.spot.entry != last {
				t.Fatalf("body click opened entry %d, want %d", got.pictures.spot.entry, last)
			}
			// Part way down, with the repeat's name above the screen.
			m.viewport.SetYOffset(m.viewport.TotalLineCount() - m.viewport.Height - 3)
			bx, by = screenCell(t, m, body, 0)
			if spot, ok := m.pictureHit(bx, by); !ok || spot.entry != last {
				t.Fatalf("scrolled body resolved to %+v %v", spot, ok)
			}
		})
	}
}

func TestPictureClickFollowsRowsAroundTheTranscript(t *testing.T) {
	cases := map[string]func(t *testing.T, m *conversationModel){
		"voice panel above": func(t *testing.T, m *conversationModel) {
			m.voiceMode = anim.ModeSpeaking
			m.reflow()
			if m.voicePanelHeight() == 0 {
				t.Fatal("voice panel not shown")
			}
		},
		"draft pictures and their error below": func(t *testing.T, m *conversationModel) {
			cmd := m.importMedia(nil, testmedia.SolidPNG(40))
			m.updateMedia(cmd())
			m.media.err = "model cannot see pictures"
			m.reflow()
			if !strings.Contains(ansi.Strip(m.View()), "model cannot see pictures") {
				t.Fatal("draft error not shown")
			}
		},
		"work strip below": func(t *testing.T, m *conversationModel) {
			m.workDisplay.terminalUntil = time.Now().Add(time.Hour)
			m.reflow()
			if m.workStripReservedRows == 0 {
				t.Fatal("work strip not shown")
			}
		},
	}
	for name, graphics := range renderers {
		for shift, apply := range cases {
			t.Run(name+"/"+shift, func(t *testing.T) {
				m, _, harbor := pictureConversation(t, 100, 44, graphics(), func(harbor chatmedia.Ref) []chatItem {
					return append(spacers(20), sys("Milo shared the harbor", harbor), sys("end"))
				})
				before := m.viewport.Height
				apply(t, &m)
				drainMedia(t, &m, m.mediaCommand())
				m.viewport.GotoBottom()
				if m.viewport.Height >= before {
					t.Fatalf("%s did not change the transcript height", shift)
				}
				x, y := screenCell(t, m, "harbor.png", 0)
				for _, row := range []int{y, y + 1} {
					got, _ := click(t, m, x+1, row)
					if !got.pictures.open || got.pictures.spot != (pictureSpot{20, 0, harbor}) {
						t.Fatalf("row %d opened %+v", row, got.pictures)
					}
				}
				if got, _ := click(t, m, x+1, y-1); got.pictures.open {
					t.Fatal("the entry above the picture opened the panel")
				}
			})
		}
	}
}

type pictureTerminal struct{ model conversationModel }

func (h *pictureTerminal) Init() tea.Cmd { return tea.EnableMouseCellMotion }
func (h *pictureTerminal) View() string  { return h.model.View() }
func (h *pictureTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if _, ok := msg.(pictureActionDone); ok {
		return h, tea.Quit
	}
	return h, cmd
}

func TestPictureClickThroughTerminalInput(t *testing.T) {
	for _, route := range []string{"mouse", "command"} {
		t.Run(route, func(t *testing.T) {
			m, rig, harbor := pictureConversation(t, 100, 30, nil, func(harbor chatmedia.Ref) []chatItem {
				return []chatItem{sys("Milo shared the harbor", harbor)}
			})
			input := "/image\r\r"
			if route == "mouse" {
				x, y := screenCell(t, m, "harbor.png", 0)
				input = fmt.Sprintf("\x1b[<0;%d;%dM\r", x+2, y+2)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var output bytes.Buffer
			h := &pictureTerminal{model: m}
			program := tea.NewProgram(h, tea.WithInput(strings.NewReader(input)), tea.WithOutput(&output), tea.WithContext(ctx), tea.WithoutSignalHandler())
			if _, err := program.Run(); err != nil {
				t.Fatal(err)
			}
			want := chatmedia.DefaultStore().Path(harbor)
			if len(rig.opened) != 1 || rig.opened[0] != want {
				t.Fatalf("opened %q, want %q", rig.opened, want)
			}
			if !strings.Contains(ansi.Strip(output.String()), "Open the original") {
				t.Fatal("panel never reached the terminal")
			}
		})
	}
}

func submit(t *testing.T, m conversationModel, text string) (conversationModel, tea.Cmd) {
	t.Helper()
	m.input.SetValue(text)
	m.syncEditorFromTextarea()
	return m.Update(panelKey("enter"))
}

func TestImageCommandOpensPanelWhileNumbersAndF3OpenTheOriginal(t *testing.T) {
	m, rig, harbor := pictureConversation(t, 100, 40, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys("first", harbor), sys("second")}
	})
	pier := storePicture(t, "pier.jpg", jpegFixture(t))
	m.items = append(m.items, sys("third", pier))
	m.refreshContent()

	for _, typed := range []string{"/image", "/ima"} {
		got, _ := submit(t, m, typed)
		if !got.pictures.open || got.pictures.spot != (pictureSpot{2, 0, pier}) {
			t.Fatalf("%s opened %+v", typed, got.pictures)
		}
		if got.input.Value() != "" || len(rig.opened) != 0 {
			t.Fatalf("%s left %q in the input or opened %q", typed, got.input.Value(), rig.opened)
		}
	}

	got, cmd := submit(t, m, "/image 1")
	if got.pictures.open || cmd == nil {
		t.Fatal("/image 1 did not open the original directly")
	}
	got, _ = got.Update(cmd())
	got, cmd = got.Update(panelKey("f3"))
	if got.pictures.open || cmd == nil {
		t.Fatal("F3 did not open the original directly")
	}
	cmd()
	store := chatmedia.DefaultStore()
	if want := []string{store.Path(harbor), store.Path(pier)}; fmt.Sprint(rig.opened) != fmt.Sprint(want) {
		t.Fatalf("opened %q, want %q", rig.opened, want)
	}

	got, _ = submit(t, m, "/image 9")
	if got.pictures.open || !strings.Contains(ansi.Strip(got.View()), "/image N (1–2)") {
		t.Fatalf("bad number was not refused:\n%s", ansi.Strip(got.View()))
	}
	empty := sizedConversation(t, 100, 30)
	empty.deps = conversationDeps{runner: &fakeTurnRunner{}, ctx: t.Context()}
	got, _ = submit(t, empty, "/image")
	if got.pictures.open || !strings.Contains(ansi.Strip(got.View()), "No picture in this conversation yet") {
		t.Fatal("/image without pictures did not say so")
	}
}

func TestPicturePanelMovesBetweenPictures(t *testing.T) {
	m, _, harbor := pictureConversation(t, 100, 40, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys("one", harbor), sys("two", harbor)}
	})
	gull := storePicture(t, "gull.png", testmedia.SolidPNG(90))
	m.items[1].attachments = append(m.items[1].attachments, gull)
	m.refreshContent()
	m, _ = submit(t, m, "/image")
	want := []pictureSpot{{0, 0, harbor}, {1, 0, harbor}, {1, 1, gull}}
	steps := []struct {
		key  string
		want int
	}{{"left", 1}, {"left", 0}, {"left", 0}, {"right", 1}, {"right", 2}, {"right", 2}}
	for _, s := range steps {
		m = pressPanel(m, panelKey(s.key))
		if m.pictures.spot != want[s.want] {
			t.Fatalf("after %s at %+v, want %+v", s.key, m.pictures.spot, want[s.want])
		}
		title := fmt.Sprintf("Picture %d of 3", s.want+1)
		if !strings.Contains(ansi.Strip(m.View()), title) {
			t.Fatalf("missing %q", title)
		}
	}
}

func TestPictureActionsUseTheirSeams(t *testing.T) {
	m, rig, harbor := pictureConversation(t, 100, 40, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys("one", harbor)}
	})
	pier := storePicture(t, "pier.jpg", jpegFixture(t))
	loop := storePicture(t, "loop.gif", testmedia.GIF())
	m.items = append(m.items, sys("two", pier, loop))
	m.refreshContent()
	store := chatmedia.DefaultStore()
	if pier.Format != "jpeg" {
		t.Fatalf("fixture format %q", pier.Format)
	}

	m, _ = submit(t, m, "/image")
	run := func(m conversationModel, action int) conversationModel {
		t.Helper()
		m.pictures.cursor = 0
		for range action {
			m = pressPanel(m, panelKey("j"))
		}
		var cmd tea.Cmd
		m, cmd = m.Update(panelKey("enter"))
		if cmd != nil {
			m, _ = m.Update(cmd())
		}
		return m
	}
	screen := func(m conversationModel) string { return ansi.Strip(m.View()) }

	for _, picture := range []struct {
		ref   chatmedia.Ref
		moves string
	}{{loop, ""}, {pier, "left"}, {harbor, "left"}} {
		if picture.moves != "" {
			m = pressPanel(m, panelKey(picture.moves))
		}
		m = run(m, pictureCopy)
		got := rig.clip.pictures[len(rig.clip.pictures)-1]
		if got != (copiedPicture{store.Path(picture.ref), picture.ref.Format}) {
			t.Fatalf("copied %+v for %s", got, picture.ref.Name)
		}
		if !strings.Contains(screen(m), "Copied the picture") {
			t.Fatal("copy result missing")
		}
	}

	m = run(m, pictureCopyPath)
	if rig.clip.text != store.Path(harbor) || !strings.Contains(screen(m), "Copied the path") {
		t.Fatalf("copied path %q", rig.clip.text)
	}
	m = run(m, pictureOpen)
	if len(rig.opened) != 1 || rig.opened[0] != store.Path(harbor) || !strings.Contains(screen(m), "Opened the original") {
		t.Fatalf("opened %q", rig.opened)
	}

	rig.clip.err = errors.New("pasteboard is busy")
	m = run(m, pictureCopy)
	if !strings.Contains(screen(m), "pasteboard is busy") {
		t.Fatal("copy failure hidden")
	}
	m.deps.clipboard = &testClipboard{}
	m = run(m, pictureCopy)
	if !strings.Contains(screen(m), "this clipboard holds only text") {
		t.Fatal("text-only clipboard not reported")
	}
}

func TestPictureSaveCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	downloads := filepath.Join(home, "Downloads")
	if err := os.Mkdir(downloads, 0o700); err != nil {
		t.Fatal(err)
	}
	m, _, harbor := pictureConversation(t, 100, 40, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys("one", harbor)}
	})
	original, err := os.ReadFile(chatmedia.DefaultStore().Path(harbor))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = submit(t, m, "/image")
	composer := m.input.Value()
	m = pressPanel(m, panelKey("j"), panelKey("j"), panelKey("j"))
	m, _ = m.Update(panelKey("enter"))
	if !m.pictures.saving || m.pictures.input.Value() != "~/Downloads/harbor.png" {
		t.Fatalf("save input = %q", m.pictures.input.Value())
	}
	save := func(m conversationModel, target string) conversationModel {
		t.Helper()
		m.pictures.input.SetValue(target)
		m, cmd := m.Update(panelKey("enter"))
		if cmd == nil {
			t.Fatal("enter did not save")
		}
		m, _ = m.Update(cmd())
		return m
	}

	target := filepath.Join(downloads, "harbor.png")
	m = save(m, "~/Downloads/harbor.png")
	if saved, err := os.ReadFile(target); err != nil || !bytes.Equal(saved, original) {
		t.Fatalf("saved copy differs: %v", err)
	}
	if m.pictures.saving || m.pictures.result != "Saved to "+target || !strings.Contains(ansi.Strip(m.View()), "Saved to ~/Downloads/harbor.png") {
		t.Fatalf("save result missing:\n%s", ansi.Strip(m.View()))
	}

	if err := os.WriteFile(target, []byte("Vera's notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = pressPanel(m, panelKey("enter"))
	m = save(m, "~/Downloads/harbor.png")
	if kept, _ := os.ReadFile(target); string(kept) != "Vera's notes" {
		t.Fatal("an existing file was overwritten")
	}
	if !m.pictures.saving || !strings.Contains(ansi.Strip(m.View()), "already exists") {
		t.Fatalf("existing file not refused:\n%s", ansi.Strip(m.View()))
	}

	m = save(m, "~/Missing/harbor.png")
	if _, err := os.Stat(filepath.Join(home, "Missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a folder was created")
	}
	if !strings.Contains(ansi.Strip(m.View()), "does not exist") {
		t.Fatalf("missing folder not refused:\n%s", ansi.Strip(m.View()))
	}

	other := filepath.Join(t.TempDir(), "copy.png")
	m = save(m, other)
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("absolute path not saved: %v", err)
	}

	folder := t.TempDir()
	m = pressPanel(m, panelKey("enter"))
	m = save(m, folder+"/")
	if _, err := os.Stat(filepath.Join(folder, "harbor.png")); err != nil {
		t.Fatalf("a folder target did not take the picture's name: %v", err)
	}
	m = pressPanel(m, panelKey("enter"))
	m = save(m, folder)
	if !m.pictures.failed || !strings.Contains(m.pictures.result, "harbor.png already exists") {
		t.Fatalf("existing file in a folder target not refused: %q", m.pictures.result)
	}

	m = pressPanel(m, panelKey("enter"))
	m = pressPanel(m, panelKey("esc"))
	if !m.pictures.open || m.pictures.saving {
		t.Fatal("esc in the save input did not return to the actions")
	}
	if m.input.Value() != composer {
		t.Fatalf("composer changed to %q", m.input.Value())
	}
}

var imageDelete = regexp.MustCompile(`a=d,d=I,i=(\d+)`)

func TestPicturePanelClosesToTheSameConversation(t *testing.T) {
	for name, graphics := range renderers {
		t.Run(name, func(t *testing.T) {
			g := graphics()
			m, _, _ := pictureConversation(t, 100, 40, g, func(harbor chatmedia.Ref) []chatItem {
				return []chatItem{sys("Milo shared the harbor", harbor), sys("Vera replied")}
			})
			var out *lockedBuffer
			if g != nil {
				out = g.out.(*lockedBuffer)
			}
			before, placed := m.View(), fmt.Sprint(m.media.placed)
			sent := 0
			if out != nil {
				sent = len(out.String())
			}

			x, y := screenCell(t, m, "harbor.png", 0)
			m, cmd := click(t, m, x, y)
			drainMedia(t, &m, cmd)
			open := m.View()
			if lipgloss.Height(open) != m.height {
				t.Fatalf("open panel is %d rows on a %d-row screen", lipgloss.Height(open), m.height)
			}
			if out != nil {
				if !strings.Contains(open, placeholderCell) || len(m.media.placed) != 2 {
					t.Fatalf("panel preview is not its own terminal image: %v", m.media.placed)
				}
			}
			m = pressPanel(m, panelKey("down"), panelKey("right"), panelKey("x"))
			m, cmd = m.Update(panelKey("esc"))
			drainMedia(t, &m, cmd)
			if m.pictures.open {
				t.Fatal("esc left the panel open")
			}
			if after := m.View(); after != before {
				t.Fatalf("conversation changed after closing:\nbefore:\n%s\nafter:\n%s", ansi.Strip(before), ansi.Strip(after))
			}
			if fmt.Sprint(m.media.placed) != placed {
				t.Fatalf("terminal images %v, want %v", m.media.placed, placed)
			}
			if out != nil {
				var deleted []string
				for _, d := range imageDelete.FindAllStringSubmatch(out.String()[sent:], -1) {
					deleted = append(deleted, d[1])
				}
				if len(deleted) != 1 {
					t.Fatalf("closing deleted %v, want only the preview", deleted)
				}
				for _, id := range m.media.placed {
					if deleted[0] == fmt.Sprint(id) {
						t.Fatal("closing deleted the transcript's picture")
					}
				}
			}
		})
	}
}

func TestPicturePanelOwnsKeysAndMouse(t *testing.T) {
	m, rig, _ := pictureConversation(t, 100, 30, nil, func(harbor chatmedia.Ref) []chatItem {
		return append(spacers(40), sys("Milo shared the harbor", harbor), sys("end"))
	})
	m.input.SetValue("half a thought")
	m.syncEditorFromTextarea()
	m.turnState = turnVoiceResponding
	m.turnCancel = func() {}
	m.viewport.GotoBottom()
	x, y := screenCell(t, m, "harbor.png", 0)
	m, _ = click(t, m, x, y)
	if !m.pictures.open {
		t.Fatal("panel did not open")
	}
	offset := m.viewport.YOffset

	m = pressPanel(m, panelKey("x"), panelKey("j"), panelKey(" "), panelKey("backspace"), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted"), Paste: true}, panelKey("k"))
	m = pressPanel(m, panelKey("j"), panelKey("j"), panelKey("j"), panelKey("enter"), panelKey("x"), panelKey("backspace"))
	if !m.pictures.saving || m.pictures.input.Value() != "~/Downloads/harbor.png" {
		t.Fatalf("save input = %q", m.pictures.input.Value())
	}
	m = pressPanel(m, panelKey("esc"))
	for _, wheel := range []tea.MouseButton{tea.MouseButtonWheelUp, tea.MouseButtonWheelDown, tea.MouseButtonWheelUp} {
		m, _ = m.Update(tea.MouseMsg{X: 10, Y: 10, Button: wheel, Action: tea.MouseActionPress})
	}
	m = pressPanel(m, panelKey("[<64;10;10M"))
	if m.input.Value() != "half a thought" {
		t.Fatalf("composer changed to %q", m.input.Value())
	}
	if rig.runner.stopped != 0 {
		t.Fatal("typing in the panel stopped speech")
	}
	if m.viewport.YOffset != offset || !m.pictures.open {
		t.Fatalf("wheel moved the transcript from %d to %d", offset, m.viewport.YOffset)
	}

	m.pictures.cursor = 0
	px, py := screenCell(t, m, "▀", 0)
	for _, row := range []int{py, py + 4} {
		if got, cmd := click(t, m, px+1, row); cmd != nil || rig.clip.text != "" || len(rig.opened) != 0 || !got.pictures.open || got.pictures.cursor != 0 {
			t.Fatal("a click on the preview ran an action")
		}
	}
	ax, ay := screenCell(t, m, "Copy its path", 0)
	m, cmd := click(t, m, ax+2, ay)
	if cmd != nil || rig.clip.text == "" || m.pictures.cursor != pictureCopyPath {
		t.Fatalf("clicking an action did not run it: %q", rig.clip.text)
	}
	g, _ := m.picturePanelLayout()
	m, _ = click(t, m, 10, g.top+g.boxRows+1)
	if m.pictures.open {
		t.Fatal("click outside the panel did not close it")
	}

	// The same keys reach the composer and stop speech once it is closed.
	m = pressPanel(m, panelKey("x"))
	m, _ = m.Update(tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.input.Value() != "half a thoughtx" || rig.runner.stopped != 1 || m.viewport.YOffset == offset {
		t.Fatalf("closed panel still held input: %q stopped=%d offset=%d", m.input.Value(), rig.runner.stopped, m.viewport.YOffset)
	}
}

func TestPicturePanelFitsTheScreen(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {100, 40}, {60, 30}, {40, 20}, {30, 10}} {
		m, _, _ := pictureConversation(t, size[0], size[1], nil, func(harbor chatmedia.Ref) []chatItem {
			return []chatItem{sys("one", harbor)}
		})
		closed := lipgloss.Height(m.View())
		m, _ = submit(t, m, "/image")
		if !m.pictures.open {
			t.Fatalf("%v: panel did not open", size)
		}
		if got := lipgloss.Height(m.View()); got != closed || got != m.height {
			t.Fatalf("%v: panel is %d rows, conversation %d", size, got, closed)
		}
		for _, line := range strings.Split(m.View(), "\n") {
			if w := ansi.StringWidth(line); w > m.width {
				t.Fatalf("%v: line %d cells wide: %q", size, w, ansi.Strip(line))
			}
		}
	}
}

func gradientPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 140, 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// The cells are counted on the rendered screen: the picture in the
// conversation, then the same picture in the panel.
func TestPicturePanelPreviewIsLarger(t *testing.T) {
	for name, graphics := range renderers {
		for shape, size := range map[string][2]int{"landscape": {480, 288}, "portrait": {288, 480}} {
			for _, screen := range [][2]int{{100, 24}, {100, 40}} {
				t.Run(fmt.Sprintf("%s/%s/%dx%d", name, shape, screen[0], screen[1]), func(t *testing.T) {
					m, _, _ := pictureConversation(t, screen[0], screen[1], graphics(), func(chatmedia.Ref) []chatItem { return nil })
					picture := storePicture(t, shape+".png", gradientPNG(t, size[0], size[1]))
					m.items = append(m.items, sys("Milo sent one", picture))
					m.refreshContent()
					drainMedia(t, &m, m.mediaCommand())
					m.viewport.GotoBottom()
					cell := bodyCell(name)
					before := strings.Count(ansi.Strip(m.View()), cell)
					m, cmd := submit(t, m, "/image")
					drainMedia(t, &m, cmd)
					after := strings.Count(ansi.Strip(m.View()), cell)
					if before == 0 || after <= before {
						t.Fatalf("panel shows %d cells, conversation %d", after, before)
					}
				})
			}
		}
	}
}

func TestPictureClickAmongEveryKindOfEntry(t *testing.T) {
	m, _, harbor := pictureConversation(t, 70, 60, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys(renderUserTurn("Milo: the harbor, and a question that runs long enough to wrap twice on a narrow screen"), harbor)}
	})
	pier := storePicture(t, "pier.png", testmedia.PNG())
	gull := storePicture(t, "gull.png", testmedia.SolidPNG(70))
	m.showThink = true
	m.appendAgentTurn(chatItem{kind: chatItemAgent, name: "Vera", think: "Looking at the light on the water, then the boats.", spoken: "Here is the pier from the ferry.", attachments: []chatmedia.Ref{pier}})
	m.appendNotice(dimStyle, "A notice long enough to wrap onto a second line with its hanging indent kept in place.")
	m.commandNotice("sealed talk · " + m.recordingLink(filepath.Join(t.TempDir(), "take one")))
	m.items = append(m.items, sys("after the notices", gull))
	m.streamingAgent = "Still writing the next reply, which wraps across the narrow screen as it streams in."
	m.refreshContent()
	drainMedia(t, &m, m.mediaCommand())
	m.viewport.GotoBottom()
	agent := slices.IndexFunc(m.items, func(it chatItem) bool { return it.kind == chatItemAgent })
	for name, want := range map[string]pictureSpot{"harbor.png": {0, 0, harbor}, "pier.png": {agent, 0, pier}, "gull.png": {len(m.items) - 1, 0, gull}} {
		m.viewport.GotoTop()
		for !m.viewport.AtBottom() {
			rows := strings.Split(ansi.Strip(m.viewport.View()), "\n")
			if i := slices.IndexFunc(rows, func(r string) bool { return strings.Contains(r, name+" · ") }); i >= 0 && i < len(rows)-2 {
				break
			}
			m.viewport.ScrollDown(1)
		}
		x, y := screenCell(t, m, name+" · ", 0)
		for _, row := range []int{y, y + 1} {
			got, _ := click(t, m, x+1, row)
			if got.pictures.spot != want {
				t.Fatalf("%s row %d opened %+v, want %+v", name, row, got.pictures.spot, want)
			}
		}
	}
}

func TestLatePictureResultGoesToTheConversation(t *testing.T) {
	m, _, _ := pictureConversation(t, 100, 40, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys("one", harbor), sys("two", harbor)}
	})
	m, _ = submit(t, m, "/image")
	_, cmd := m.Update(panelKey("enter"))
	m = pressPanel(m, panelKey("left"))
	m, _ = m.Update(cmd())
	if strings.Contains(ansi.Strip(m.View()), "Opened the original") {
		t.Fatal("a result for the other picture was shown on this one")
	}
	m = pressPanel(m, panelKey("esc"))
	if !strings.Contains(ansi.Strip(m.View()), "Opened the original") {
		t.Fatal("late result was lost")
	}
}

// clipboardTools puts stand-in tools on a PATH that holds nothing else. Each
// records its arguments and input, so every OS's commands are checked on any
// host and nothing reaches the real clipboard.
func clipboardTools(t *testing.T, scripts map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stand-in tools are shell scripts")
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	for name, body := range scripts {
		script := fmt.Sprintf("#!/bin/sh\nLOG=%q\n%s\n", log, body)
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	return log
}

func TestWriteImageClipboardCommands(t *testing.T) {
	record := func(name string, exit int) string {
		return fmt.Sprintf("{ printf '%%s|' %s \"$@\"; /bin/cat; echo; } >> \"$LOG\"\nexit %d", name, exit)
	}
	log := clipboardTools(t, map[string]string{"osascript": record("osascript", 0), "wl-copy": record("wl-copy", 1), "xclip": record("xclip", 0)})
	picture := filepath.Join(t.TempDir(), "pier with spaces.jpg")
	if err := os.WriteFile(picture, []byte("jpeg bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := func(goos string) string {
		t.Helper()
		_ = os.Remove(log)
		if err := writeImageClipboardOn(t.Context(), goos, picture, "jpeg"); err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		got, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}
	if got := calls("darwin"); !strings.Contains(got, "osascript|-e|") || !strings.Contains(got, "as «class JPEG»") || !strings.Contains(got, "|"+picture+"|") {
		t.Fatalf("osascript call = %q", got)
	}
	// wl-copy fails here, so xclip is tried with the same picture.
	if got := calls("linux"); !strings.Contains(got, "wl-copy|--type|image/jpeg|jpeg bytes") || !strings.Contains(got, "xclip|-selection|clipboard|-t|image/jpeg|-i|jpeg bytes") {
		t.Fatalf("clipboard tool calls = %q", got)
	}
	if err := writeImageClipboardOn(t.Context(), "windows", picture, "jpeg"); err == nil || !strings.Contains(err.Error(), "copy its path") {
		t.Fatalf("windows copy = %v", err)
	}
}

// A picture copied from the panel must paste back into the composer, so the
// paste asks the clipboard for every format the copy can put there.
func TestClipboardPasteAsksForEveryFormatACopyWrites(t *testing.T) {
	if len(pictureFormats) != len(pictureMIME) || len(pictureFormats) != len(appleScriptPictureClass) {
		t.Fatalf("formats %v, MIME types %v and pasteboard classes %v differ", pictureFormats, pictureMIME, appleScriptPictureClass)
	}
	for _, format := range pictureFormats {
		t.Run(format, func(t *testing.T) {
			only := fmt.Sprintf("for a in \"$@\"; do if [ \"$a\" = %q ]; then printf '%s picture'; exit 0; fi; done\nexit 1", pictureMIME[format], format)
			for _, tool := range []string{"wl-paste", "xclip"} {
				clipboardTools(t, map[string]string{tool: only})
				got, err := readImageClipboard(t.Context(), "linux")
				if err != nil || string(got) != format+" picture" {
					t.Fatalf("%s with a %s on the clipboard pasted %q, %v", tool, format, got, err)
				}
			}
			if !strings.Contains(clipboardReadScript(), "the clipboard as "+appleScriptPictureClass[format]) {
				t.Fatalf("macOS paste does not ask for %s:\n%s", format, clipboardReadScript())
			}
		})
	}
	log := clipboardTools(t, map[string]string{"osascript": "printf '%s' \"$2\" > \"$LOG\"\nprintf 'pasted picture' > \"$3\""})
	got, err := readImageClipboard(t.Context(), "darwin")
	if err != nil || string(got) != "pasted picture" {
		t.Fatalf("macOS paste = %q, %v", got, err)
	}
	if script, _ := os.ReadFile(log); string(script) != clipboardReadScript() {
		t.Fatalf("osascript ran %q", script)
	}
}

// On a screen too short for the whole panel, what the user is acting on stays:
// the save field with its error, and otherwise the selected action.
func TestPicturePanelKeepsTheSaveFieldOnAShortScreen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, size := range [][2]int{{30, 10}, {40, 12}, {60, 9}} {
		m, _, _ := pictureConversation(t, size[0], size[1], nil, func(harbor chatmedia.Ref) []chatItem {
			return []chatItem{sys("one", harbor)}
		})
		m, _ = submit(t, m, "/image")
		m = pressPanel(m, panelKey("j"), panelKey("j"), panelKey("j"))
		if screen := ansi.Strip(m.View()); !strings.Contains(screen, "› Save a copy") {
			t.Fatalf("%v: selected action hidden:\n%s", size, screen)
		}
		x, y := screenCell(t, m, "Save a copy", 0)
		clicked, _ := click(t, m, x, y)
		if !clicked.pictures.saving {
			t.Fatalf("%v: a click on the action where it is drawn did not run it", size)
		}
		m = pressPanel(m, panelKey("enter"))
		if screen := ansi.Strip(m.View()); !m.pictures.saving || !strings.Contains(screen, "Save as ") {
			t.Fatalf("%v: save field hidden:\n%s", size, screen)
		}
		m.pictures.input.SetValue("~/Missing/harbor.png")
		m, cmd := m.Update(panelKey("enter"))
		m, _ = m.Update(cmd())
		screen := ansi.Strip(m.View())
		if !strings.Contains(screen, "Save as ") || !strings.Contains(screen, "does not exist") {
			t.Fatalf("%v: save field or its error hidden:\n%s", size, screen)
		}
		if got := lipgloss.Height(m.View()); got != m.height {
			t.Fatalf("%v: panel is %d rows", size, got)
		}
	}
}

// AppleScript refuses some ordinary words as variable names when it compiles a
// script, and a script that does not compile pastes or copies nothing without
// saying why. Compiling runs nothing and leaves the clipboard alone.
func TestClipboardScriptsCompile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("AppleScript is compiled on macOS only")
	}
	scripts := map[string]string{"paste": clipboardReadScript()}
	for format, class := range appleScriptPictureClass {
		scripts["copy "+format] = clipboardWriteScript(class)
	}
	for name, script := range scripts {
		out, err := exec.CommandContext(t.Context(), "/usr/bin/osacompile", "-o", filepath.Join(t.TempDir(), "script.scpt"), "-e", script).CombinedOutput()
		if err != nil {
			t.Errorf("%s script does not compile: %v: %s", name, err, out)
		}
	}
}

func TestPicturePanelShowsALongFailureOnOneRow(t *testing.T) {
	m, rig, _ := pictureConversation(t, 100, 40, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys("one", harbor)}
	})
	m, _ = submit(t, m, "/image")
	rig.clip.err = fmt.Errorf("copy the picture (install wl-clipboard or xclip): %w", errors.Join(errors.New("wl-copy: exit status 1"), errors.New("xclip: not found")))
	m = pressPanel(m, panelKey("j"))
	m, cmd := m.Update(panelKey("enter"))
	m, _ = m.Update(cmd())
	if got := lipgloss.Height(m.View()); got != m.height {
		t.Fatalf("panel is %d rows on a %d-row screen", got, m.height)
	}
	if screen := ansi.Strip(m.View()); !strings.Contains(screen, "wl-copy: exit status 1 xclip: not found") {
		t.Fatalf("failure not shown whole:\n%s", screen)
	}
}

func TestPictureSaveTreatsATrailingSlashAsAFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.SetConfigDirForTest(t, t.TempDir())
	harbor := storePicture(t, "harbor.png", harborFixture(t))
	if _, err := savePictureCopy(chatmedia.DefaultStore(), harbor, "~/Pictures/"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing folder typed with a slash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "Pictures")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a file was written under the folder's name")
	}
}

func TestPictureSaveInputScrollsAndIgnoresASecondEnter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o700); err != nil {
		t.Fatal(err)
	}
	m, _, _ := pictureConversation(t, 60, 30, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys("one", harbor)}
	})
	m, _ = submit(t, m, "/image")
	m = pressPanel(m, panelKey("j"), panelKey("j"), panelKey("j"), panelKey("enter"))
	if m.pictures.input.Width <= 0 {
		t.Fatal("save input has no width, so a long path cannot scroll to its cursor")
	}
	for _, r := range "-with-a-name-long-enough-to-run-past-the-right-edge-of-the-box.png" {
		m = pressPanel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if screen := ansi.Strip(m.View()); !strings.Contains(screen, "edge-of-the-box.png") {
		t.Fatalf("the end of the path being typed is off screen:\n%s", screen)
	}
	m.pictures.input.SetValue("~/Downloads/harbor.png")
	m, first := m.Update(panelKey("enter"))
	m, second := m.Update(panelKey("enter"))
	if first == nil || second != nil {
		t.Fatalf("first save started=%v, second started=%v", first != nil, second != nil)
	}
	m, _ = m.Update(first())
	if m.pictures.failed || m.pictures.busy || !strings.HasPrefix(m.pictures.result, savedPrefix) {
		t.Fatalf("result = %q failed=%v busy=%v", m.pictures.result, m.pictures.failed, m.pictures.busy)
	}
}

func TestCtrlCClosesThePicturePanelAndKeepsTheClipboard(t *testing.T) {
	m, rig, harbor := pictureConversation(t, 100, 40, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys("one", harbor)}
	})
	m.lastAssistantText = "Vera's last reply"
	m, _ = submit(t, m, "/image")
	m = pressPanel(m, panelKey("j"), panelKey("j"), panelKey("enter"))
	path := chatmedia.DefaultStore().Path(harbor)
	if rig.clip.text != path {
		t.Fatalf("clipboard = %q", rig.clip.text)
	}
	a := App{screen: screenConversation, conversation: m}
	a, _, handled := a.onKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !handled || a.conversation.pictures.open {
		t.Fatalf("ctrl+c handled=%v, panel open=%v", handled, a.conversation.pictures.open)
	}
	if rig.clip.text != path {
		t.Fatalf("ctrl+c replaced the copied path with %q", rig.clip.text)
	}
}
