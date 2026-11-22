package brain

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
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
			before := historyState(o)
			o.viewImage(t.Context(), tc.file)
			after := historyState(o)
			last := len(after) - 1
			for i := range last {
				if after[i] != before[i] {
					t.Fatalf("history[%d] changed: %+v -> %+v", i, before[i], after[i])
				}
			}
			if got := len(o.working[last].OpenedImages); got != tc.opened || after[last].images != tc.opened {
				t.Fatalf("the retried message holds %d opened pictures and %d images", got, after[last].images)
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
	before := historyState(o)
	result, _ := o.viewImage(t.Context(), "again.png")
	w := lastUserTurn(o.working)
	if !strings.Contains(result, "already attached") || !strings.Contains(result, postedByYou) || len(o.working[w].OpenedImages) != 0 {
		t.Fatalf("result %q, opened %d", result, len(o.working[w].OpenedImages))
	}
	if after := historyState(o); after[w] != before[w] {
		t.Fatalf("the message changed: %+v -> %+v", before[w], after[w])
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
	m := o.history[lastUserIndex(o.history)]
	if !strings.Contains(result, "is now attached") || len(m.Images) != 2 || !strings.Contains(m.Content, pictureLine(chart, attached, postedByYou)) {
		t.Fatalf("result %q; the message carries %d images: %q", result, len(m.Images), m.Content)
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
