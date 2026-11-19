package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

type harnessFixtureBrain struct {
	*routedProjectionBrain
	classify   func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error)
	requests   []brain.LiveRouteRequest
	chat       []brain.StreamOptions
	chatInputs []string
}

func (b *harnessFixtureBrain) ClassifyLiveRoute(ctx context.Context, r brain.LiveRouteRequest) (brain.StreamResult, error) {
	b.requests = append(b.requests, r)
	return b.classify(ctx, r)
}
func (b *harnessFixtureBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.chat = append(b.chat, opts)
	b.chatInputs = append(b.chatInputs, input)
	return b.fakeBrain.ThinkStream(ctx, input, opts)
}
func harnessPipeline(t *testing.T, tiered bool) (*Pipeline, *harnessFixtureBrain, *job.Table) {
	t.Helper()
	b := &harnessFixtureBrain{routedProjectionBrain: &routedProjectionBrain{fakeBrain: &fakeBrain{chunks: []string{"Conversation answer."}}, turns: []brain.Turn{{Role: "user", Content: "Use Denver, Colorado."}}}}
	b.classify = func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
		return brain.StreamResult{Route: "spawn", RouteTier: config.WorkerTierSimple, RouteReasonCode: "bounded_action"}, nil
	}
	p := &Pipeline{Brain: b, Events: events.NewBus(), VoiceToolsEnabled: true}
	p.SetOutputMuted(true)
	cfg := &config.Config{WorkDir: t.TempDir(), PromptsDir: t.TempDir(), VoiceToolsEnabled: true, VoiceWorkerProvider: "ollama"}
	if tiered {
		cfg = tierTestConfig()
		cfg.WorkDir, cfg.PromptsDir = t.TempDir(), t.TempDir()
	}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "Denver forecast. Source: https://example.com/forecast"}, cfg, nil)
	a.Wire()
	a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
		return &lifecycleWorker{reply: "Denver forecast. Source: https://example.com/forecast"}, nil
	}
	return p, b, jobs
}

func TestHarnessNaturalLookupRoutesThroughTextAndVoiceAdmission(t *testing.T) {
	for _, voice := range []bool{false, true} {
		for _, tiered := range []bool{false, true} {
			name := map[bool]string{false: "text", true: "voice"}[voice] + map[bool]string{false: "-legacy", true: "-tiered"}[tiered]
			t.Run(name, func(t *testing.T) {
				p, b, jobs := harnessPipeline(t, tiered)
				const query = "Can you tell me what the weather is today?"
				p.STT = &fakeSTT{text: query}
				finished := make(chan events.JobFinished, 2)
				events.Subscribe(p.Events, func(e events.JobFinished) { finished <- e })
				var route RouteDecision
				p.OnRoute = func(r RouteDecision) { route = r }
				store := session.NewStore(t.TempDir())
				saved := session.New("codex", "fixture")
				p.OnTurn = func() {
					if err := store.Save(saved, b.History()); err != nil {
						t.Error(err)
					}
				}
				var err error
				if voice {
					_, err = p.RunTurn(t.Context())
				} else {
					err = p.RunTurnTextMode(t.Context(), query)
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case result := <-finished:
					if !strings.Contains(result.Summary, "https://example.com/forecast") {
						t.Fatal(result)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("worker was not dispatched")
				}
				if len(b.requests) != 1 || len(b.chat) != 0 || len(jobs.Status()) != 1 || route.Kind != RouteSpawn || route.Transcript != query {
					t.Fatalf("requests=%d chat=%d route=%+v jobs=%+v", len(b.requests), len(b.chat), route, jobs.Status())
				}
				if tiered && route.Tier != config.WorkerTierSimple {
					t.Fatalf("lost tier: %+v", route)
				}
				if len(b.requests[0].Conversation) != 1 || !strings.Contains(b.requests[0].Conversation[0].Content, "Denver") {
					t.Fatal("location was lost")
				}
				loaded, err := store.Load(saved.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded.Turns) < 2 || loaded.Turns[1].Content != query {
					t.Fatalf("routed request not saved: %+v", loaded.Turns)
				}
				for _, turn := range loaded.Turns {
					if strings.Contains(turn.Content, `"kind":"spawn"`) {
						t.Fatal("private routing JSON entered history")
					}
				}
			})
		}
	}
}

func TestHarnessRoutingChatAndPolicyBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     brain.StreamOptions
		classify bool
	}{
		{"ordinary", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly}, true},
		{"tools off", brain.StreamOptions{RouterToolsPolicy: brain.RouterToolsRouteOnly}, false},
		{"legacy", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsLegacy}, false},
		{"background", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly, Background: true}, false},
		{"continuation", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly, AgentContinuation: true}, false},
		{"discussion", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly, Discussion: &discussion.Directive{}}, false},
		{"summary", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly, OmitTurnInstruction: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, b, jobs := harnessPipeline(t, false)
			b.classify = func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
				return brain.StreamResult{}, nil
			}
			s, err := p.startBrainStream(t.Context(), "What about web search?", tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			for range s.Chunks {
			}
			if r := <-s.Done; r.Err != nil {
				t.Fatal(r.Err)
			}
			if (len(b.requests) == 1) != tc.classify || len(b.chat) != 1 || len(jobs.Status()) != 0 {
				t.Fatalf("routing escaped policy: requests=%d chat=%d", len(b.requests), len(b.chat))
			}
			if (b.chat[0].HarnessRouting != nil) != tc.classify {
				t.Fatal("capability guidance missing or escaped its turn")
			}
		})
	}
}

func TestHarnessRoutingFailuresNeverDispatchOrSpeakDenial(t *testing.T) {
	for _, mode := range []string{"inference", "cancel", "persona change", "unavailable tier"} {
		t.Run(mode, func(t *testing.T) {
			p, b, jobs := harnessPipeline(t, true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.classify = func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
				switch mode {
				case "inference":
					return brain.StreamResult{}, errors.New("invalid route JSON")
				case "cancel":
					cancel()
				case "persona change":
					p.Brain = &fakeBrain{}
				case "unavailable tier":
					return brain.StreamResult{Route: "spawn", RouteTier: config.WorkerTierComplex}, nil
				}
				return brain.StreamResult{Route: "spawn", RouteTier: config.WorkerTierSimple}, nil
			}
			_ = p.RunTurnTextMode(ctx, "Can you check the forecast?")
			if len(jobs.Status()) != 0 || len(b.chat) != 0 {
				t.Fatal("failed routing reached worker or talking model")
			}
		})
	}
}

func TestHarnessExplicitSearchDoesNotDoubleDispatch(t *testing.T) {
	p, b, jobs := harnessPipeline(t, false)
	if err := p.RunTurnTextMode(t.Context(), "search the web for current Denver weather"); err != nil {
		t.Fatal(err)
	}
	if len(b.requests) != 0 || len(b.chat) != 0 || len(jobs.Status()) != 1 {
		t.Fatal("deterministic route was classified or dispatched twice")
	}
}

func TestMonkeyPictureAfterCatDispatchesOnce(t *testing.T) {
	for _, mode := range []string{"text", "voice"} {
		t.Run(mode, func(t *testing.T) {
			p, b, jobs := harnessPipeline(t, false)
			cfg, worker, _, _, _, release := imageWorkerFixture(t, false)
			close(release)
			p.Agency.ReplaceWorker(worker, cfg, nil)
			finished := make(chan events.JobFinished, 1)
			events.Subscribe(p.Events, func(e events.JobFinished) { finished <- e })
			b.turns = []brain.Turn{
				{Role: "assistant", Content: "I can't fetch or show images directly in this chat."},
				{Role: "assistant", Content: "Cat picture", Attachments: []chatmedia.Ref{{Name: "cat.jpg"}}},
			}
			const query = "Can you show me a picture of a monkey?"
			var route RouteDecision
			p.OnRoute = func(d RouteDecision) { route = d }
			var err error
			if mode == "voice" {
				p.STT = &fakeSTT{text: query}
				_, err = p.RunTurn(t.Context())
			} else {
				err = p.RunTurnTextMode(t.Context(), query)
			}
			if err != nil || route.Transcript != query || route.Kind != RouteSpawn || len(jobs.Status()) != 1 || len(b.requests) != 0 || len(b.chat) != 0 {
				t.Fatalf("route=%+v jobs=%v classifiers=%d chat=%d err=%v", route, jobs.Status(), len(b.requests), len(b.chat), err)
			}
			select {
			case result := <-finished:
				if len(result.Attachments) != 1 {
					t.Fatalf("worker returned no image: %+v", result)
				}
				if _, err := chatmedia.DefaultStore().Preview(result.Attachments[0]); err != nil {
					t.Fatalf("cannot display returned image: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("image worker did not finish")
			}
		})
	}
}

func TestPictureJobTextAloneCannotAnnounceDelivery(t *testing.T) {
	for _, reply := range []string{
		"I can't actually fetch or display images in this chat mode. I can only show you files that already exist here.",
		"I found some nice monkey pictures for you. Here you go.",
		"I tried both image sources. Neither was online.",
	} {
		p, b, _ := harnessPipeline(t, false)
		b.turns = []brain.Turn{{Role: "user", Content: "Show me an image of a monkey please"}, {Role: "assistant", Content: "I found some monkey pictures."}}
		p.Agency.ReplaceWorker(&lifecycleWorker{reply: reply}, p.Agency.currentConfig(), nil)
		failed := make(chan events.JobFailed, 1)
		events.Subscribe(p.Events, func(e events.JobFailed) { failed <- e })
		if err := p.RunTurnTextMode(t.Context(), "You need to add them to the chat, please."); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-failed:
			if !strings.Contains(e.Err, errNoImageDelivered.Error()) || strings.Count(e.Err, reply) != 1 {
				t.Fatal(e.Err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("attachment-free reply was accepted")
		}
	}
}
