package brain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

const missingDogImages = "I searched for a dog image and found a suitable PNG and GIF. I’ve attached them below."

func TestUnsupportedImageResult(t *testing.T) {
	o := &OllamaBrain{}
	for _, text := range []string{
		missingDogImages,
		"They're attached here: a PNG file and a GIF file located in the working directory.",
		"I'll get those PNG images attached and send them over.",
		"I attached a PNG.\n```markdown\n![Example](</not-a-result.png>)\n```",
	} {
		if !o.unsupportedImageResult(text, "") {
			t.Errorf("accepted missing image: %q", text)
		}
	}
	for _, text := range []string{
		"I couldn't attach an image because the download failed.",
		"PNG is a lossless image format.",
		"I attached a PDF.",
		"I can attach an image if you want one.",
		"The phrase “I attached a PNG” is an example.",
		"```text\nI attached a PNG.\n```",
		"> I attached a PNG.\nThat claim was wrong.",
		"I attached the image.\n![Dog](</work/dog.png>)",
	} {
		if o.unsupportedImageResult(text, "") {
			t.Errorf("rejected ordinary or explicit reply: %q", text)
		}
	}
	o.canonical = []Turn{{Role: "assistant", Attachments: []chatmedia.Ref{{ID: "existing"}}}}
	if o.unsupportedImageResult("I attached a PNG earlier.", "") {
		t.Fatal("ignored existing attachment evidence")
	}
	if !o.unsupportedImageResult(missingDogImages, "") {
		t.Fatal("an earlier image excused a new unsupported attachment claim")
	}
}

func imageClaimBrain(t *testing.T, replies []api.Message) (*OllamaBrain, *[]api.ChatRequest) {
	t.Helper()
	var requests []api.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, req)
		if len(requests) > len(replies) {
			t.Error("unbounded image correction")
			http.Error(w, "too many calls", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: replies[len(requests)-1], Done: true, Metrics: api.Metrics{PromptEvalCount: 10, EvalCount: 5}})
	}))
	t.Cleanup(srv.Close)
	host, _ := url.Parse(srv.URL)
	cfg := &config.Config{AgentName: "Obey", MaxHistory: 20, VoiceToolsEnabled: true, PromptsDir: t.TempDir()}
	o := &OllamaBrain{client: api.NewClient(host, srv.Client()), model: "fixture", cfg: cfg}
	return o, &requests
}

func TestImageClaimRepairRoutesBeforeSpeechAndKeepsTier(t *testing.T) {
	o, requests := imageClaimBrain(t, []api.Message{
		{Role: "assistant", Content: "I’ve attached them below."},
		{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "spawn", "tier": "medium", "reason_code": "dependent_steps"})}}}},
	})
	usage := 0
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, WorkerTiers: &WorkerTierAvailability{Medium: true}, OnUsage: func(int, int) { usage++ }}
	stream, err := o.ThinkStream(t.Context(), "Find a PNG of a dog and show it here.", opts)
	if err != nil {
		t.Fatal(err)
	}
	chunks, _ := drainStream(t, stream)
	result := <-stream.Done
	if result.Err != nil || result.Route != "spawn" || result.RouteTier != config.WorkerTierMedium || result.RouteReasonCode != "dependent_steps" {
		t.Fatalf("repair lost route or tier: %+v", result)
	}
	if chunks != "" || len(o.History()) != 0 {
		t.Fatalf("unverified claim reached speech/history: %q %+v", chunks, o.History())
	}
	if len(*requests) != 2 || usage != 2 || len((*requests)[1].Tools) == 0 {
		t.Fatalf("repair accounting: requests=%d usage=%d", len(*requests), usage)
	}
	if !strings.Contains((*requests)[1].Messages[0].Content, "Nothing was delivered to the chat") {
		t.Fatal("missing correction instruction")
	}
}

func TestImageClaimRepairIsBoundedAndAllowsHonestFailure(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, honest := range []bool{false, true} {
			second := missingDogImages
			if honest {
				second = "I couldn't obtain the image, so nothing was attached."
			}
			replies := []api.Message{{Role: "assistant", Content: missingDogImages}}
			if honest {
				// Even an honest refusal must satisfy main's route-first contract.
				replies = append(replies, api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "chat"})}}}})
			}
			replies = append(replies, api.Message{Role: "assistant", Content: second})
			o, requests := imageClaimBrain(t, replies)
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
			var reply string
			var err error
			if streaming {
				stream, startErr := o.ThinkStream(t.Context(), "Show me a dog image.", opts)
				if startErr != nil {
					t.Fatal(startErr)
				}
				reply, _ = drainStream(t, stream)
				err = (<-stream.Done).Err
			} else {
				reply, err = o.ThinkFull(t.Context(), "Show me a dog image.", opts)
			}
			if len(*requests) != len(replies) {
				t.Fatalf("requests=%d", len(*requests))
			}
			if honest {
				if err != nil || reply != second {
					t.Fatalf("honest failure: %q %v", reply, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "no image was attached") || reply != "" {
				t.Fatalf("unverified claim escaped: %q %v", reply, err)
			}
			for _, turn := range o.History() {
				if strings.Contains(turn.Content, missingDogImages) {
					t.Fatal("rejected claim polluted history")
				}
			}
		}
	}
}

func TestImageAndMissingRouteShareOneRetry(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, imageFirst := range []bool{false, true} {
			first, second := "I'll look that up.", missingDogImages
			if imageFirst {
				first, second = second, first
			}
			o, requests := imageClaimBrain(t, []api.Message{{Role: "assistant", Content: first}, {Role: "assistant", Content: second}})
			usage := 0
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, OnUsage: func(int, int) { usage++ }}
			var reply string
			var err error
			if streaming {
				stream, startErr := o.ThinkStream(t.Context(), "Find an image.", opts)
				if startErr != nil {
					t.Fatal(startErr)
				}
				reply, _ = drainStream(t, stream)
				err = (<-stream.Done).Err
			} else {
				reply, err = o.ThinkFull(t.Context(), "Find an image.", opts)
			}
			if err == nil || reply != "" || len(*requests) != 2 || usage != 2 {
				t.Fatalf("streaming=%t imageFirst=%t reply=%q error=%v requests=%d usage=%d", streaming, imageFirst, reply, err, len(*requests), usage)
			}
		}
	}
}

func TestImageClaimAfterChatRoute(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range []struct {
			name, input, answer string
			blocked             bool
		}{
			{"named image", "Show me a dog image.", "I attached a dog image.", true},
			{"pronoun", "Find a PNG of a dog.", "I’ve attached them below.", true},
			{"refusal", "Show me a dog image.", "I couldn't obtain it, so nothing was attached.", false},
			{"explicit reference", "Show me a dog image.", "I attached it.\n![Dog](</work/dog.png>)", false},
			{"unrelated attachment", "Where is the PDF?", "I’ve attached it below.", false},
			{"quoted claim", "Explain this image caption.", "The phrase “I’ve attached them below” is not proof.", false},
		} {
			t.Run(tc.name+map[bool]string{false: "/blocking", true: "/streaming"}[streaming], func(t *testing.T) {
				o, requests := imageClaimBrain(t, []api.Message{
					{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "chat"})}}}},
					{Role: "assistant", Content: tc.answer},
				})
				usage := 0
				opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, OnUsage: func(int, int) { usage++ }}
				var reply string
				var err error
				if streaming {
					stream, startErr := o.ThinkStream(t.Context(), tc.input, opts)
					if startErr != nil {
						t.Fatal(startErr)
					}
					reply, _ = drainStream(t, stream)
					err = (<-stream.Done).Err
				} else {
					reply, err = o.ThinkFull(t.Context(), tc.input, opts)
				}
				if len(*requests) != 2 || usage != 2 || len((*requests)[1].Tools) != 0 {
					t.Fatalf("post-chat accounting: requests=%d usage=%d", len(*requests), usage)
				}
				if tc.blocked {
					if err == nil || !strings.Contains(err.Error(), "no image was attached") || reply != "" {
						t.Fatalf("unsupported claim escaped: reply=%q error=%v", reply, err)
					}
					for _, turn := range o.History() {
						if strings.Contains(turn.Content, tc.answer) {
							t.Fatal("unsupported claim entered history")
						}
					}
				} else if err != nil || reply != tc.answer {
					t.Fatalf("valid answer rejected: reply=%q error=%v", reply, err)
				}
			})
		}
	}
}
