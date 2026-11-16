package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func stubPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageEstimateFollowsPatchGrid(t *testing.T) {
	for _, tc := range []struct{ w, h, want int }{
		{28, 28, 1 + imageTokenOverhead},
		{29, 28, 2 + imageTokenOverhead},
		{1474, 2012, 53*72 + imageTokenOverhead},
		{2048, 2048, 74*74 + imageTokenOverhead},
		{4096, 4096, 147*147 + imageTokenOverhead},
	} {
		if got := estimateImageTokens(stubPNG(t, tc.w, tc.h)); got != tc.want {
			t.Errorf("%dx%d: %d tokens, want %d", tc.w, tc.h, got, tc.want)
		}
	}
	if got := estimateImageTokens([]byte("not an image")); got != maxImageTokens {
		t.Errorf("unreadable image: %d tokens, want %d", got, maxImageTokens)
	}
}

// visionFake answers route rounds with a route call and everything else with
// a short reply, and records every chat request it receives.
type visionFake struct {
	mu       sync.Mutex
	requests []api.ChatRequest
}

func (f *visionFake) brain(t *testing.T, cfg *config.Config) *OllamaBrain {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "vision", "tools"}})
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		seq := len(f.requests)
		f.mu.Unlock()
		msg := api.Message{Role: "assistant", Content: "It is a grey rectangle."}
		if hasTool(req.Tools, "route") {
			args := api.NewToolCallFunctionArguments()
			args.Set("kind", "chat")
			msg = api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{ID: fmt.Sprintf("call-%d", seq), Function: api.ToolCallFunction{Name: "route", Arguments: args}}}}
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: req.Model, Message: msg, Done: true, DoneReason: "stop"})
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	return &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: "You are Obey.", cfg: cfg}
}

func (f *visionFake) sent() []api.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.ChatRequest(nil), f.requests...)
}

// promptCost prices a request the way a vision model with the given cost per
// image would see it: text at four bytes a token, each image at imageCost.
// The price is independent of the estimator under test.
func promptCost(req api.ChatRequest, imageCost int) int {
	text := make([]api.Message, len(req.Messages))
	images := 0
	for i, m := range req.Messages {
		text[i] = m
		text[i].Images = nil
		images += len(m.Images)
	}
	return estimateMessageTokens(text, req.Tools) + images*imageCost
}

func imagesIn(messages []api.Message) int {
	n := 0
	for _, m := range messages {
		n += len(m.Images)
	}
	return n
}

func ownerBudgetConfig() *config.Config {
	cfg := budgetConfig()
	cfg.ContinuityNumCtxCeil = 32768
	cfg.ContinuityIntegrityEnabled = true
	return cfg
}

// Every turn attaches a photo. Before each turn the pipeline's soft limit
// check runs, falling back to trimming as a failed summary does. Each request
// is priced at what a real model spent on that image (muse-glimmer for the
// photo, qwen3.8 for the largest image), and none may exceed the window.
func TestConsecutiveImageTurnsStayWithinContextWindow(t *testing.T) {
	for _, img := range []struct {
		name       string
		w, h, cost int
	}{
		{"photo", 1474, 2012, 3694},
		{"largest image", 4096, 4096, 16386},
	} {
		for _, pipeline := range []bool{true, false} {
			name := img.name + map[bool]string{true: ", soft limit before each turn", false: ", provider guard only"}[pipeline]
			t.Run(name, func(t *testing.T) {
				config.SetConfigDirForTest(t, t.TempDir())
				ref, err := chatmedia.DefaultStore().Import("photo.png", stubPNG(t, img.w, img.h))
				if err != nil {
					t.Fatal(err)
				}
				fake := &visionFake{}
				cfg := ownerBudgetConfig()
				o := fake.brain(t, cfg)
				softEngaged, maxImages := 0, 0
				for n := 1; n <= 16; n++ {
					input := fmt.Sprintf("photo-%02d: what is in this one?", n)
					if pipeline && o.NeedsContinuityCompact(input) {
						softEngaged++
						o.TrimContinuity(input)
					}
					before := len(fake.sent())
					turn := runRoutedTurn(t, o, input, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, Attachments: []chatmedia.Ref{ref}})
					if turn.result.Err != nil {
						t.Fatalf("turn %d: %v", n, turn.result.Err)
					}
					sent := fake.sent()[before:]
					if len(sent) == 0 {
						t.Fatalf("turn %d sent nothing", n)
					}
					for _, req := range sent {
						if cost := promptCost(req, img.cost); cost > cfg.OllamaNumCtx {
							t.Fatalf("turn %d sent %d tokens with %d images, above num_ctx %d", n, cost, imagesIn(req.Messages), cfg.OllamaNumCtx)
						}
						if last := req.Messages[len(req.Messages)-1]; last.Role == "user" && len(last.Images) != 1 {
							t.Fatalf("turn %d: the newest image was not sent with its turn", n)
						}
						maxImages = max(maxImages, imagesIn(req.Messages))
					}
					assertWholeGroups(t, o.history)
					assertWorkingAligned(t, o)
				}
				if maxImages < 2 && img.cost < 5000 {
					t.Fatalf("at most %d photo kept; earlier photos should stay while they fit", maxImages)
				}
				if pipeline && img.cost < 5000 && softEngaged == 0 {
					t.Fatal("soft limit never engaged on a run of image turns")
				}
				if len(turnBlocks(o)) == 0 {
					t.Fatal("evicted image turns were not archived")
				}
			})
		}
	}
}

// A turn's own images are sent as before, however large the estimate.
func TestFreshTurnWithLargestImagesIsSent(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var refs []chatmedia.Ref
	for i := range chatmedia.MaxAttachments {
		data := stubPNG(t, 4096, 4096)
		ref, err := chatmedia.DefaultStore().Import(fmt.Sprintf("large-%d.png", i), append(data, byte(i)))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	fake := &visionFake{}
	o := fake.brain(t, ownerBudgetConfig())
	turn := runRoutedTurn(t, o, "Compare these.", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, Attachments: refs})
	if turn.result.Err != nil {
		t.Fatalf("turn refused: %v", turn.result.Err)
	}
	sent := fake.sent()
	if len(sent) != 2 || imagesIn(sent[0].Messages) != len(refs) || imagesIn(sent[1].Messages) != len(refs) {
		t.Fatalf("sent %d requests; images per request did not match the %d attached", len(sent), len(refs))
	}
}

func TestSingleImageStaysInPlainConversation(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref, err := chatmedia.DefaultStore().Import("photo.png", stubPNG(t, 1474, 2012))
	if err != nil {
		t.Fatal(err)
	}
	fake := &visionFake{}
	o := fake.brain(t, ownerBudgetConfig())
	for n := 1; n <= archiveHistoryTurnsHigh; n++ {
		input := fmt.Sprintf("turn-%02d", n)
		if o.NeedsContinuityCompact(input) {
			t.Fatalf("turn %d: soft limit engaged for one image in a short conversation", n)
		}
		opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
		if n == 1 {
			opts.Attachments = []chatmedia.Ref{ref}
		}
		if turn := runRoutedTurn(t, o, input, opts); turn.result.Err != nil {
			t.Fatalf("turn %d: %v", n, turn.result.Err)
		}
	}
	requests := fake.sent()
	for i, req := range requests {
		if imagesIn(req.Messages) != 1 {
			t.Fatalf("request %d carries %d images, want the one retained image", i, imagesIn(req.Messages))
		}
	}
	if userTurns(o.history) != archiveHistoryTurnsHigh || len(turnBlocks(o)) != 0 {
		t.Fatalf("kept %d turns, archived %d blocks", userTurns(o.history), len(turnBlocks(o)))
	}
}
