package tui

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi/kitty"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func importPicture(t *testing.T, data []byte) chatmedia.Ref {
	t.Helper()
	r, err := chatmedia.DefaultStore().Import("", data)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func workerPictureItem(refs ...chatmedia.Ref) pipeline.WorkerInfo {
	return pipeline.WorkerInfo{ID: "picture-task", Task: "Review campaign changes", State: "running", Provider: "ollama", Model: "test-local",
		Thread: []brain.Turn{{Role: "user", Content: "Review campaign changes", Attachments: refs}}}
}

// Runs a command tree to completion and applies every media message. Paused
// GIFs keep animation timers out of it.
func drainWorkers(t *testing.T, m *workersModel, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			drainWorkers(t, m, c)
		}
	case mediaLoaded, mediaPlaced:
		next, c := m.Update(msg)
		*m = next
		drainWorkers(t, m, c)
	}
}

// Like drainWorkers through App.Update, which also returns timers; a command
// that does not finish quickly is one of those and is dropped.
func drainApp(t *testing.T, app App, cmd tea.Cmd) App {
	t.Helper()
	if cmd == nil {
		return app
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(2 * time.Second):
		return app
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			app = drainApp(t, app, c)
		}
	case mediaLoaded, mediaPlaced:
		model, next := app.Update(msg)
		app = drainApp(t, model.(App), next)
	}
	return app
}

func openWorkerPicture(t *testing.T, g *mediaGraphics, refs ...chatmedia.Ref) workersModel {
	t.Helper()
	m := newWorkers(&config.Config{}, &testWorkers{items: []pipeline.WorkerInfo{workerPictureItem(refs...)}})
	m.adoptGraphics(g)
	m.media.paused = true
	m, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	drainWorkers(t, &m, cmd)
	m.cursor = 2
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	drainWorkers(t, &m, cmd)
	if m.detailID == "" {
		t.Fatal("detail did not open")
	}
	return m
}

func deleted(stream string, id int) bool {
	return strings.Contains(stream, "a=d,d=I,i="+strconv.Itoa(id)+",")
}

func showsPlaceholders(view string, id int, box mediaBox) bool {
	for _, line := range strings.Split(mediaPlaceholders(id, box.cols, box.rows), "\n") {
		if !strings.Contains(view, line) {
			return false
		}
	}
	return true
}

var transmittedID = regexp.MustCompile(`(?:^|,)i=(\d+)(?:,|$)`)

func TestWorkerDetailShowsGraphicsPictureNatively(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importPicture(t, testmedia.PNG())
	g, out := testGraphics(2, 4)
	m := openWorkerPicture(t, g, ref)

	w, h := m.previewSize(1)
	box := g.box(ref, w, h)
	if h != previewRows(true, m.detail.Height, 1) || box.rows <= 8 {
		t.Fatalf("box %dx%d does not exercise the row budget", box.cols, box.rows)
	}
	keys, data := sentImage(t, out.String())
	match := transmittedID.FindStringSubmatch(keys)
	if match == nil {
		t.Fatalf("no image was transmitted: %q", keys)
	}
	id, _ := strconv.Atoi(match[1])
	if m.media.placed[box] != id {
		t.Fatalf("placed %v, transmitted id %d", m.media.placed, id)
	}
	view := m.View()
	if !showsPlaceholders(view, id, box) || !strings.ContainsRune(view, kitty.Placeholder) {
		t.Fatalf("worker view has no placeholders for image %d:\n%q", id, view)
	}
	if strings.Contains(view, "▀") {
		t.Fatal("worker view still draws the picture with half-blocks")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() > box.cols*2 || b.Dy() > box.rows*4 {
		t.Fatalf("transmitted %dx%d pixels for a %dx%d cell box", b.Dx(), b.Dy(), box.cols, box.rows)
	}
}

func TestWorkerDetailHalfBlocksUseTheChatRowBudget(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importPicture(t, testmedia.PNG())
	m := openWorkerPicture(t, nil, ref)
	want := previewRows(false, m.detail.Height, 1)
	if want == 8 {
		t.Fatal("pane height does not tell the budget from eight rows")
	}
	rows := 0
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "▀") {
			rows++
		}
	}
	if rows != want {
		t.Fatalf("half-block picture is %d rows, want %d", rows, want)
	}
	if strings.ContainsRune(m.View(), kitty.Placeholder) {
		t.Fatal("placeholders without graphics")
	}
}

func TestWorkerDetailKeepsGIFsOnHalfBlocksWithGraphics(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importPicture(t, testmedia.GIF())
	g, out := testGraphics(2, 4)
	m := openWorkerPicture(t, g, ref)
	view := m.View()
	if !strings.Contains(view, "▀") || strings.ContainsRune(view, kitty.Placeholder) || out.String() != "" {
		t.Fatalf("animated GIF left half-blocks: sent %q\n%q", out.String(), view)
	}
}

type pictureApp struct {
	app                App
	g                  *mediaGraphics
	out                *lockedBuffer
	chatRef, workerRef chatmedia.Ref
	chatBox            mediaBox
	chatID             int
}

// A chat showing chatData natively, switched to the Workers screen with a
// task whose request carries workerData.
func newPictureApp(t *testing.T, chatData, workerData []byte) pictureApp {
	t.Helper()
	m, _ := mediaTestModel(t)
	m.media.draft = nil
	chatRef := importPicture(t, chatData)
	m.items = append(m.items, chatItem{kind: chatItemSys, sys: "sent", attachments: []chatmedia.Ref{chatRef}})
	m.reflow()
	g, out := testGraphics(2, 4)
	m.adoptGraphics(g)
	drainMedia(t, &m, m.mediaCommand())
	chatBox := g.box(chatRef, max(1, m.viewport.Width-4), m.threadPreviewRows(1))
	p := pictureApp{g: g, out: out, chatRef: chatRef, chatBox: chatBox, chatID: m.media.placed[chatBox]}
	if p.chatID == 0 {
		t.Fatalf("chat picture was not placed: %v", m.media.placed)
	}
	p.workerRef = importPicture(t, workerData)
	app := newApp(m.cfg, nil, nil)
	app.width, app.height = 100, 40
	app.screen = screenConversation
	app.conversation = m
	model, _ := app.Update(switchScreenMsg(screenWorkers))
	app = model.(App)
	app.workers.controller = &testWorkers{items: []pipeline.WorkerInfo{workerPictureItem(p.workerRef)}}
	app.workers.media.paused = true
	app.workers.refresh()
	p.app = app
	return p
}

func (p *pictureApp) update(t *testing.T, msg tea.Msg) {
	t.Helper()
	model, cmd := p.app.Update(msg)
	p.app = drainApp(t, model.(App), cmd)
}

func (p *pictureApp) openDetail(t *testing.T) (mediaBox, int) {
	t.Helper()
	p.app.workers.cursor = 2
	p.update(t, tea.KeyMsg{Type: tea.KeyEnter})
	w, h := p.app.workers.previewSize(1)
	box := p.g.box(p.workerRef, w, h)
	id := p.app.workers.media.placed[box]
	if id == 0 || id == p.chatID {
		t.Fatalf("worker picture id %d (chat %d), placed %v", id, p.chatID, p.app.workers.media.placed)
	}
	if !showsPlaceholders(p.app.View(), id, box) {
		t.Fatalf("worker detail lacks its placeholders:\n%q", p.app.View())
	}
	return box, id
}

func (p *pictureApp) assertChatIntact(t *testing.T) {
	t.Helper()
	if deleted(p.out.String(), p.chatID) {
		t.Fatal("the worker view freed the chat's picture")
	}
	if p.g.sent[p.chatBox] != p.chatID {
		t.Fatal("the terminal no longer holds the chat's picture")
	}
}

func TestWorkerPicturesLeaveTheChatPicturesInPlace(t *testing.T) {
	for _, leave := range []string{"close detail, then screen", "screen with detail open"} {
		t.Run(leave, func(t *testing.T) {
			p := newPictureApp(t, testmedia.SolidPNG(40), testmedia.PNG())
			_, workerID := p.openDetail(t)
			p.assertChatIntact(t)

			if leave == "close detail, then screen" {
				p.update(t, tea.KeyMsg{Type: tea.KeyEsc})
				if !deleted(p.out.String(), workerID) {
					t.Fatal("closing the detail kept its picture in the terminal")
				}
				model, cmd := p.app.Update(tea.KeyMsg{Type: tea.KeyEsc})
				p.app = model.(App)
				p.update(t, cmd())
			} else {
				p.update(t, switchScreenMsg(screenConversation))
				if !deleted(p.out.String(), workerID) {
					t.Fatal("leaving the Workers screen kept its picture in the terminal")
				}
			}
			if p.app.screen != screenConversation {
				t.Fatalf("screen = %v", p.app.screen)
			}
			p.assertChatIntact(t)
			c := p.app.conversation
			native, ok := c.nativeImage(p.chatRef, max(1, c.viewport.Width-4), c.threadPreviewRows(1))
			if !ok || !strings.Contains(strings.Join(c.transcript, "\n"), native) {
				t.Fatal("the chat lost its placeholders")
			}
		})
	}
}

// Leaving for the launcher on the way back to the menu sets the screen
// directly, so the next Workers screen is the first to see these images.
func TestFreshWorkerScreenFreesEarlierWorkerPictures(t *testing.T) {
	p := newPictureApp(t, testmedia.SolidPNG(40), testmedia.PNG())
	_, workerID := p.openDetail(t)
	p.app.screen = screenLauncher
	p.update(t, switchScreenMsg(screenWorkers))
	p.update(t, workersTickMsg{p.app.workers.token})
	if !deleted(p.out.String(), workerID) {
		t.Fatal("a fresh Workers screen kept the earlier screen's picture")
	}
	p.assertChatIntact(t)
}

func TestGraphicsSharedBoxIsSentOnceAndHeldWhileWanted(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importPicture(t, testmedia.PNG())
	g, out := testGraphics(2, 4)
	store := chatmedia.DefaultStore()
	box := g.box(ref, 96, 24)
	want := []mediaWant{{box, ref}}
	placed, _ := g.sync(mediaOwnerConversation, g.nextSync(), want, store)
	id := placed[box]
	again, _ := g.sync(mediaOwnerWorkers, g.nextSync(), want, store)
	if id == 0 || again[box] != id || strings.Count(out.String(), "a=T") != 1 {
		t.Fatalf("a box both owners want was sent %d times", strings.Count(out.String(), "a=T"))
	}
	g.sync(mediaOwnerConversation, g.nextSync(), nil, store)
	if deleted(out.String(), id) {
		t.Fatal("freed while the worker view still wants it")
	}
	g.sync(mediaOwnerConversation, g.nextSync(), want, store)
	g.sync(mediaOwnerWorkers, g.nextSync(), nil, store)
	if deleted(out.String(), id) || strings.Count(out.String(), "a=T") != 1 {
		t.Fatal("freed or resent while the chat still wants it")
	}
	g.sync(mediaOwnerConversation, g.nextSync(), nil, store)
	if !deleted(out.String(), id) {
		t.Fatal("not freed once no owner wants it")
	}
}

func TestGraphicsPictureInChatAndWorkerViewIsSentOnce(t *testing.T) {
	p := newPictureApp(t, testmedia.PNG(), testmedia.PNG())
	p.app.workers.cursor = 2
	p.update(t, tea.KeyMsg{Type: tea.KeyEnter})
	w, h := p.app.workers.previewSize(1)
	if box := p.g.box(p.workerRef, w, h); box != p.chatBox {
		t.Fatalf("worker box %v differs from chat box %v", box, p.chatBox)
	}
	if p.app.workers.media.placed[p.chatBox] != p.chatID || strings.Count(p.out.String(), "a=T") != 1 {
		t.Fatalf("shared picture sent %d times", strings.Count(p.out.String(), "a=T"))
	}
	if !showsPlaceholders(p.app.View(), p.chatID, p.chatBox) {
		t.Fatal("worker view does not show the shared picture")
	}
	p.update(t, switchScreenMsg(screenConversation))
	p.assertChatIntact(t)
}

func TestGraphicsOrderingIsPerOwner(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importPicture(t, testmedia.PNG())
	g, out := testGraphics(2, 4)
	store := chatmedia.DefaultStore()
	box := g.box(ref, 96, 24)
	older, workerSeq, newer := g.nextSync(), g.nextSync(), g.nextSync()
	placed, ok := g.sync(mediaOwnerConversation, newer, []mediaWant{{box, ref}}, store)
	if !ok || placed[box] == 0 {
		t.Fatal("newer request was not applied")
	}
	if _, ok := g.sync(mediaOwnerConversation, older, nil, store); ok || deleted(out.String(), placed[box]) {
		t.Fatal("an older request for the same owner undid a newer one")
	}
	small := g.box(ref, 20, 10)
	got, ok := g.sync(mediaOwnerWorkers, workerSeq, []mediaWant{{small, ref}}, store)
	if !ok || got[small] == 0 || g.sent[box] != placed[box] || deleted(out.String(), placed[box]) {
		t.Fatalf("another owner's request was dropped or disturbed the first: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("an owner was told about images it did not ask for: %v", got)
	}
}

func TestMediaPlacedReachesItsOwnerAcrossScreens(t *testing.T) {
	m, _ := mediaTestModel(t)
	app := newApp(m.cfg, nil, nil)
	app.width, app.height = 100, 40
	app.screen = screenConversation
	app.conversation = m
	model, _ := app.Update(switchScreenMsg(screenWorkers))
	app = model.(App)
	chatBox, workerBox := mediaBox{id: "chat", cols: 1, rows: 1}, mediaBox{id: "worker", cols: 1, rows: 1}

	model, _ = app.Update(mediaPlaced{token: app.conversation.media.token, placed: map[mediaBox]int{chatBox: 5}})
	app = model.(App)
	if app.conversation.media.placed[chatBox] != 5 || app.workers.media.placed != nil {
		t.Fatalf("chat sync behind Workers: chat %v, workers %v", app.conversation.media.placed, app.workers.media.placed)
	}
	model, _ = app.Update(mediaPlaced{token: app.workers.media.token, placed: map[mediaBox]int{workerBox: 6}})
	app = model.(App)
	if app.workers.media.placed[workerBox] != 6 || app.conversation.media.placed[workerBox] != 0 {
		t.Fatalf("worker sync: chat %v, workers %v", app.conversation.media.placed, app.workers.media.placed)
	}

	workerToken := app.workers.media.token
	model, _ = app.Update(switchScreenMsg(screenConversation))
	app = model.(App)
	model, _ = app.Update(mediaPlaced{token: workerToken, placed: map[mediaBox]int{workerBox: 7}})
	app = model.(App)
	if len(app.conversation.media.placed) != 1 || app.conversation.media.placed[chatBox] != 5 {
		t.Fatalf("chat took the worker's sync: %v", app.conversation.media.placed)
	}
}

func TestWorkerPictureFollowsAResize(t *testing.T) {
	p := newPictureApp(t, testmedia.SolidPNG(40), testmedia.PNG())
	oldBox, oldID := p.openDetail(t)
	sent := len(p.out.String())
	p.update(t, tea.WindowSizeMsg{Width: 60, Height: 40})
	w, h := p.app.workers.previewSize(1)
	box := p.g.box(p.workerRef, w, h)
	if box == oldBox {
		t.Fatal("resize does not change the picture's box")
	}
	id := p.app.workers.media.placed[box]
	stream := p.out.String()[sent:]
	if id == 0 || !strings.Contains(stream, "c="+strconv.Itoa(box.cols)+",") {
		t.Fatalf("picture was not sent at the new box %dx%d: %q", box.cols, box.rows, stream)
	}
	if !deleted(stream, oldID) {
		t.Fatal("the old box was not freed")
	}
	if !showsPlaceholders(p.app.View(), id, box) {
		t.Fatal("worker view does not show the resized picture")
	}
}

func TestHiddenChatPictureFollowsAResize(t *testing.T) {
	p := newPictureApp(t, testmedia.PNG(), testmedia.SolidPNG(40))
	p.update(t, tea.WindowSizeMsg{Width: 60, Height: 40})
	c := p.app.conversation
	box := p.g.box(p.chatRef, max(1, c.viewport.Width-4), c.threadPreviewRows(1))
	if box == p.chatBox {
		t.Fatal("resize does not change the chat picture's box")
	}
	if c.media.placed[box] == 0 || !deleted(p.out.String(), p.chatID) {
		t.Fatalf("hidden chat did not resend its picture at the new box: placed %v", c.media.placed)
	}
}

type workerPictureTerminal struct {
	app     App
	initial tea.Cmd
	step    int
	placed  bool
	delay   time.Duration
	record  bool
}

type workerPictureStep struct{}

func (h *workerPictureTerminal) Init() tea.Cmd { return tea.Batch(h.initial, h.next()) }
func (h *workerPictureTerminal) next() tea.Cmd {
	return tea.Tick(h.delay, func(time.Time) tea.Msg { return workerPictureStep{} })
}
func (h *workerPictureTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	if _, ok := msg.(workerPictureStep); ok {
		h.step++
		switch {
		case h.step <= 2:
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case h.step == 3:
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case h.record && h.step < 8:
			return h, h.next()
		case h.record || h.placed || h.step > 100:
			return h, tea.Quit
		default:
			// One more frame after the picture is placed, so it is drawn.
			h.placed = len(h.app.workers.media.placed) > 0
			return h, h.next()
		}
		next = h.next()
	}
	model, cmd := h.app.Update(msg)
	h.app = model.(App)
	return h, tea.Batch(cmd, next)
}
func (h *workerPictureTerminal) View() string { return h.app.View() }

// The real App and Bubble Tea renderer carry the worker picture's
// placeholder rows to the terminal. OBEY_VOICE_WORKER_PICTURE_TERMINAL=record
// plays it on the real terminal instead, on half-blocks: the recording
// terminal has no Kitty graphics.
func TestWorkerPicturePlaceholdersReachTheTerminal(t *testing.T) {
	record := os.Getenv("OBEY_VOICE_WORKER_PICTURE_TERMINAL") == "record"
	m, _ := mediaTestModel(t)
	ref := m.media.draft[0]
	m.media.draft = nil
	m.reflow()
	g, _ := testGraphics(2, 4)
	if record {
		g = nil
	}
	m.adoptGraphics(g)
	app := newApp(m.cfg, nil, nil)
	app.width, app.height = 100, 40
	app.screen = screenConversation
	app.conversation = m
	model, initial := app.Update(switchScreenMsg(screenWorkers))
	app = model.(App)
	app.workers.controller = &testWorkers{items: []pipeline.WorkerInfo{workerPictureItem(ref)}}
	app.workers.refresh()
	h := &workerPictureTerminal{app: app, initial: initial, delay: 50 * time.Millisecond, record: record}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var output bytes.Buffer
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if record {
		h.delay = 900 * time.Millisecond
		opts = append(opts, tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	} else {
		opts = append(opts, tea.WithInput(nil), tea.WithOutput(&output))
	}
	if _, err := tea.NewProgram(h, opts...).Run(); err != nil {
		t.Fatal(err)
	}
	if record {
		return
	}
	w, rows := h.app.workers.previewSize(1)
	box := g.box(ref, w, rows)
	id := h.app.workers.media.placed[box]
	if id == 0 {
		t.Fatalf("worker picture was never placed: %v", h.app.workers.media.placed)
	}
	if !showsPlaceholders(output.String(), id, box) {
		t.Fatalf("renderer output lacks the placeholder rows for image %d:\n%q", id, output.String())
	}
}
