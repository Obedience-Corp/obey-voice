package brain

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// Lines the user types that look like picture lines are the user's words.
func TestUserLinesThatLookLikePictureLinesAreKept(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	fake := &visionFake{}
	o := fake.brain(t, budgetConfig(), "You are Obey.")
	input := "My notes:\nPicture 1 attached: the front door\nPicture 2 no longer attached: the back\nPicture 12ab attached: the side"
	for _, in := range []string{input, "t2", "t3", "t4"} {
		if _, err := o.ThinkFull(t.Context(), in, StreamOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for i, req := range fake.sent() {
		if m := messageWith(t, req, "My notes:"); !strings.Contains(m.Content, input) {
			t.Fatalf("request %d sent %q", i, m.Content)
		}
	}
	if got := o.ContextHistory()[0].Content; got != input {
		t.Fatalf("the projected user turn reads %q", got)
	}
	o.archiveTurns(o.history[:2], o.working[:2])
	entries := o.ContinuityArchive()
	if note := entries[len(entries)-1].Summary; !strings.Contains(note, "Picture 2 no longer attached: the back") {
		t.Fatalf("archive note = %q", note)
	}
}

func noisyPNG(t *testing.T, path string, side int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	x := uint32(7)
	for i := 0; i < side*side; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		img.Set(i%side, i/side, color.RGBA{uint8(x), uint8(x >> 8), uint8(x >> 16), 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

type messageState struct {
	content string
	images  int
}

func historyState(o *OllamaBrain) []messageState {
	var out []messageState
	for _, m := range o.history {
		out = append(out, messageState{m.Content, len(m.Images)})
	}
	return out
}

// built is the request the brain would send now.
func built(o *OllamaBrain) api.ChatRequest {
	return api.ChatRequest{Messages: o.buildMessages("")}
}

// requestState is each message of the request the brain would send now.
func requestState(o *OllamaBrain) []messageState {
	var out []messageState
	for _, m := range o.buildMessages("") {
		out = append(out, messageState{m.Content, len(m.Images)})
	}
	return out
}

// A retried input keeps the image window its request was prepared with;
// view_image does not bring back a picture that window left out.
func TestViewImageOnARetriedInputKeepsTheRequestWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		file   string
		write  func(*testing.T, string)
		limit  bool
		opened int
	}{
		"fails":    {"big.png", func(t *testing.T, p string) { noisyPNG(t, p, 96) }, true, 0},
		"succeeds": {"small.png", func(t *testing.T, p string) { writePNG(t, p) }, false, 1},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &visionFake{}
			o, chart := postedPicture(t, fake)
			o.workDir = t.TempDir()
			tc.write(t, filepath.Join(o.workDir, tc.file))
			for _, in := range []string{"t2", "t3"} {
				if _, err := o.ThinkFull(t.Context(), in, StreamOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := o.appendUserInput("t4", StreamOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := o.prepareImageHistory(t.Context(), StreamOptions{RetryInput: true}); err != nil {
				t.Fatal(err)
			}
			if tc.limit {
				o.maxImageBytes = modelImageSize(t, chart)
			}
			before, stored := requestState(o), historyState(o)
			o.viewImage(t.Context(), tc.file)
			after := requestState(o)
			last := len(after) - 1
			if len(after) != len(before) || !strings.HasPrefix(after[last].content, "t4") {
				t.Fatalf("the request changed shape: %+v -> %+v", before, after)
			}
			for i := range last {
				if after[i] != before[i] {
					t.Fatalf("request message %d changed: %+v -> %+v", i, before[i], after[i])
				}
			}
			if got := len(o.working[lastUserTurn(o.working)].OpenedImages); got != tc.opened || after[last].images != tc.opened {
				t.Fatalf("the retried message holds %d opened pictures and %d images", got, after[last].images)
			}
			if !slices.Equal(historyState(o), stored) {
				t.Fatalf("the prompt history changed: %+v -> %+v", stored, historyState(o))
			}
		})
	}
}

// A picture already on the message as a reply picture is not opened again.
func TestViewImageOfAPictureAlreadyPosted(t *testing.T) {
	fake := &visionFake{}
	o, chart := postedPicture(t, fake)
	o.workDir = t.TempDir()
	data, err := os.ReadFile(chatmedia.DefaultStore().Path(chart))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.workDir, "again.png"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := o.appendUserInput("What is it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	before := requestState(o)
	result, _ := o.viewImage(t.Context(), "again.png")
	w := lastUserTurn(o.working)
	if !strings.Contains(result, "already attached") || !strings.Contains(result, postedByYou) || len(o.working[w].OpenedImages) != 0 {
		t.Fatalf("result %q, opened %d", result, len(o.working[w].OpenedImages))
	}
	if after := requestState(o); !slices.Equal(after, before) {
		t.Fatalf("the request changed: %+v -> %+v", before, after)
	}
}

// With the prompt history and the conversation out of line, view_image says
// so and changes neither.
func TestViewImageWhenTheViewsDisagree(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, _ := toolModel(t, true, nil)
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	o.appendMessage(api.Message{Role: "user", Content: "Look at shot.png."}, nil)
	o.history = append([]api.Message{{Role: "user", Content: "Earlier."}}, o.history...)
	before := historyState(o)
	result, _ := o.viewImage(t.Context(), "shot.png")
	if !strings.Contains(result, "could not be attached") || strings.Contains(result, "no user message") {
		t.Fatalf("result %q", result)
	}
	if after := historyState(o); len(after) != len(before) || after[0] != before[0] || after[1] != before[1] || len(o.working[0].OpenedImages) != 0 {
		t.Fatalf("history %+v -> %+v", before, after)
	}
}

// A capability check that view_image makes itself counts, though the one made
// for the request failed.
func TestViewImageAfterAFailedCapabilityCheck(t *testing.T) {
	fake := &visionFake{}
	o, chart := postedPicture(t, fake)
	o.workDir = t.TempDir()
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	fake.showFails.Store(true)
	if err := o.prepareImageHistory(t.Context(), StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := o.appendUserInput("Look at shot.png.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	fake.showFails.Store(false)
	result, _ := o.viewImage(t.Context(), "shot.png")
	m := messageWith(t, built(o), "Look at shot.png.")
	if !strings.Contains(result, "is now attached") || len(m.Images) != 2 || !strings.Contains(m.Content, pictureLine(chart, attached, postedByYou)) {
		t.Fatalf("result %q; the message carries %d images: %q", result, len(m.Images), m.Content)
	}
}

// Consecutive requests, the rounds of one turn and the first round of the
// next, send the same bytes for every message before the latest user message
// except where a picture ages out.
func TestCarriedPicturesKeepEarlierMessagesStable(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, func(w http.ResponseWriter, _ int, req api.ChatRequest) {
		latest := req.Messages[len(req.Messages)-1]
		switch {
		case latest.Role == "user" && strings.Contains(latest.Content, "Look at shot.png."):
			callTool(w, req, "view_image", map[string]any{"path": "shot.png"})
		case latest.Role == "user":
			callTool(w, req, "list_files", map[string]any{"path": "."})
		default:
			answer(w, req, api.Message{Role: "assistant", Content: "Done."}, "stop")
		}
	})
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	if _, err := o.ThinkFull(t.Context(), "Look at shot.png.", directTools); err != nil {
		t.Fatal(err)
	}
	if err := o.SetReplyMedia("Done.\nchart", photos(t, 1, 32, 32)); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"turn-2", "turn-3", "turn-4", "turn-5"} {
		if _, err := o.ThinkFull(t.Context(), input, directTools); err != nil {
			t.Fatal(err)
		}
	}
	requests := sent()
	if len(requests) != 10 {
		t.Fatalf("%d requests", len(requests))
	}
	var breaks []int
	for k := 1; k < len(requests); k++ {
		prev, next := requests[k-1].Messages, requests[k].Messages
		p := min(len(prev), lastUserIndex(next))
		if jsonOf(t, prev[:p]) != jsonOf(t, next[:p]) {
			breaks = append(breaks, k)
		}
	}
	// The opened picture ages out with turn-4's first request, the posted one
	// with turn-5's.
	if !slices.Equal(breaks, []int{6, 8}) {
		t.Fatalf("requests changed an earlier message at %v, want only [6 8]", breaks)
	}
	for _, m := range o.history {
		if m.Role == "user" && (strings.Contains(m.Content, "\nPicture ") || len(m.Images) > 0) {
			t.Fatalf("the prompt history holds a carried picture: %q with %d images", m.Content, len(m.Images))
		}
	}
}

// Opening again a picture opened for an earlier message attaches it to the
// latest one when only one copy fits, and does not say it was pushed out.
func TestViewImageReopensAPictureOpenedForAnEarlierMessage(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, func(w http.ResponseWriter, _ int, req api.ChatRequest) {
		if req.Messages[len(req.Messages)-1].Role == "user" {
			callTool(w, req, "view_image", map[string]any{"path": "shot.png"})
			return
		}
		answer(w, req, api.Message{Role: "assistant", Content: "A grey rectangle."}, "stop")
	})
	writePNG(t, filepath.Join(o.workDir, "shot.png"))
	if _, err := o.ThinkFull(t.Context(), "Look at shot.png.", directTools); err != nil {
		t.Fatal(err)
	}
	ref := o.History()[0].OpenedImages[0]
	o.maxImageBytes = modelImageSize(t, ref)
	if _, err := o.ThinkFull(t.Context(), "Look at it again.", directTools); err != nil {
		t.Fatal(err)
	}
	requests := sent()
	last := requests[len(requests)-1]
	var result string
	for _, m := range last.Messages {
		if m.Role == "tool" && m.ToolName == "view_image" {
			result = m.Content
		}
	}
	if !strings.Contains(result, "is now attached") || strings.Contains(result, "no longer attached") {
		t.Fatalf("tool result = %q", result)
	}
	var again Turn
	for _, turn := range o.History() {
		if turn.Role == "user" && turn.Content == "Look at it again." {
			again = turn
		}
	}
	if len(again.OpenedImages) != 1 || again.OpenedImages[0].ID != ref.ID {
		t.Fatalf("the latest user turn reads %+v", again)
	}
	if m := messageWith(t, last, "Look at it again."); len(m.Images) != 1 || strings.Count(m.Content, openedLine(ref)) != 1 {
		t.Fatalf("the latest message carries %d images: %q", len(m.Images), m.Content)
	}
	if m := messageWith(t, last, "Look at shot.png."); len(m.Images) != 0 || !strings.Contains(m.Content, pictureLine(ref, agedOut, openedByYou)) {
		t.Fatalf("the earlier message carries %d images: %q", len(m.Images), m.Content)
	}
}

// Opening a picture that leaves no room for one opened before says which one
// is no longer attached.
func TestViewImageSaysWhichPictureItPushedOut(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, func(w http.ResponseWriter, n int, req api.ChatRequest) {
		switch n {
		case 1:
			callTool(w, req, "view_image", map[string]any{"path": "first.png"})
		case 2:
			callTool(w, req, "view_image", map[string]any{"path": "second.png"})
		default:
			answer(w, req, api.Message{Role: "assistant", Content: "Done."}, "stop")
		}
	})
	noisyPNG(t, filepath.Join(o.workDir, "first.png"), 24)
	noisyPNG(t, filepath.Join(o.workDir, "second.png"), 25)
	sizes := 0
	for _, name := range []string{"first.png", "second.png"} {
		info, err := os.Stat(filepath.Join(o.workDir, name))
		if err != nil {
			t.Fatal(err)
		}
		sizes = max(sizes, int(info.Size()))
	}
	o.maxImageBytes = sizes + 1
	if _, err := o.ThinkFull(t.Context(), "Compare first.png and second.png.", directTools); err != nil {
		t.Fatal(err)
	}
	var result string
	for _, m := range sent()[2].Messages {
		if m.Role == "tool" && m.ToolName == "view_image" {
			result = m.Content
		}
	}
	if !strings.Contains(result, "(second.png) is now attached") || !strings.Contains(result, "(first.png) is no longer attached") {
		t.Fatalf("second result %q", result)
	}
}
