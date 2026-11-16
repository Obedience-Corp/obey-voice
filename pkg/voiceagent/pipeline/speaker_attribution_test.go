package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	admissionpolicy "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

type speakerRecordingBrain struct {
	fakeBrain
	mu       sync.Mutex
	speakers []string
}

func (b *speakerRecordingBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.speakers = append(b.speakers, opts.Speaker)
	b.mu.Unlock()
	return b.fakeBrain.ThinkStream(ctx, input, opts)
}

// speakerTrace is what one turn recorded about its speaker on every surface
// other than the prompt text.
type speakerTrace struct {
	event, admission, stored string
	brain                    []string
}

// turnPaths are the ways a user turn reaches history: a brain reply, a local
// reply from admission, and a deterministic route that skips the brain.
var turnPaths = []string{"brain", "local", "routed"}

func runSpeakerTurn(t *testing.T, voice bool, path, mic string) speakerTrace {
	t.Helper()
	transcript := &recordedTranscript{}
	input := "what's for dinner"
	if path == "routed" {
		input = "search the web for rust"
	}
	b := &speakerRecordingBrain{fakeBrain: fakeBrain{chunks: []string{"Soup tonight."}}}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript, CurrentSpeaker: func() string { return mic }, OnRoute: func(RouteDecision) {}}
	if voice {
		p.STT, p.VAD, p.Capture = &fakeSTT{text: input}, &fakeVAD{}, newFakeCapture()
	}
	var trace speakerTrace
	events.Subscribe(p.Events, func(e events.UserInput) { trace.event = e.Speaker })
	p.AdmitInput = func(ctx context.Context, input string, _ bool) (ResponseAdmission, error) {
		trace.admission = admissionpolicy.Speaker(ctx)
		envelope := events.ResponseEnvelope{SessionID: "s", ResponseID: "r", PersonaID: "mira", DisplayName: "Mira"}
		if path == "local" {
			return ResponseAdmission{LocalReply: "Mira here.", Envelope: envelope}, nil
		}
		return ResponseAdmission{Request: input, Envelope: envelope}, nil
	}
	var err error
	if voice {
		_, err = p.RunTurn(t.Context())
	} else {
		err = p.RunTurnTextMode(t.Context(), input)
	}
	if err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) == 0 || turns[0].Role != "user" || turns[0].Content != input {
		t.Fatalf("transcript = %+v", turns)
	}
	trace.stored = turns[0].Speaker
	b.mu.Lock()
	trace.brain = append([]string(nil), b.speakers...)
	b.mu.Unlock()
	if path != "brain" && len(trace.brain) != 0 {
		t.Fatalf("%s path reached the brain", path)
	}
	return trace
}

func TestTypedTurnNeverInheritsTheMicrophoneLabel(t *testing.T) {
	for _, mic := range []string{"Lance", "unknown", "speaker-2"} {
		for _, path := range turnPaths {
			trace := runSpeakerTurn(t, false, path, mic)
			if trace.event != "" || trace.admission != "" || trace.stored != "" {
				t.Errorf("mic %q, %s path: typed turn took a speaker: %+v", mic, path, trace)
			}
			for _, s := range trace.brain {
				if s != "" {
					t.Errorf("mic %q: brain was told typed speaker %q", mic, s)
				}
			}
		}
	}
}

func TestVoiceTurnKeepsItsLabelOnEveryRecord(t *testing.T) {
	for _, mic := range []string{"Lance", "unknown", "speaker-2"} {
		for _, path := range turnPaths {
			trace := runSpeakerTurn(t, true, path, mic)
			if trace.event != mic || trace.admission != mic || trace.stored != mic {
				t.Errorf("mic %q, %s path: voice label changed: %+v", mic, path, trace)
			}
			if path == "brain" && (len(trace.brain) != 1 || trace.brain[0] != mic) {
				t.Errorf("mic %q: brain speakers = %q", mic, trace.brain)
			}
		}
	}
}

func TestSpeakerGateStillSeesTheUnidentifiedLabel(t *testing.T) {
	var gated []string
	b := &countingBrain{fakeBrain: fakeBrain{chunks: []string{"must never run"}}}
	p := &Pipeline{
		STT: &fakeSTT{text: "what is the wifi password"}, Brain: b, VAD: &fakeVAD{}, Capture: newFakeCapture(), Events: events.NewBus(),
		CurrentSpeaker: func() string { return "unknown" },
		SpeakerGate: func(label string) string {
			gated = append(gated, label)
			return "utterance ignored: unverified speaker " + label
		},
	}
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(gated) != 1 || gated[0] != "unknown" {
		t.Fatalf("gate labels = %q", gated)
	}
	if n := b.streams.Load(); n != 0 {
		t.Fatalf("brain ran %d time(s) for a vetoed unidentified speaker", n)
	}
}

// An unidentified voice turn through a real Ollama brain: the label stays on
// the event and in history, and no /api/chat request names it.
func TestUnidentifiedVoiceTurnRequestCarriesNoSpeakerName(t *testing.T) {
	for _, tc := range []struct{ mic, wantUser string }{
		{"unknown", "what's for dinner"},
		{"", "what's for dinner"},
		{"Lance", "Lance: what's for dinner"},
		{"speaker-2", "speaker-2: what's for dinner"},
	} {
		t.Run(tc.mic, func(t *testing.T) {
			var mu sync.Mutex
			var requests []api.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/chat" {
					_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
					return
				}
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				mu.Lock()
				requests = append(requests, req)
				mu.Unlock()
				_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"Soup tonight."},"done":true}`+"\n")
			}))
			defer server.Close()
			o, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", PromptsDir: t.TempDir(), WorkDir: t.TempDir(), UserDisplayName: "Lance"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = brain.CloseProvider(o) }()
			var shown string
			p := &Pipeline{STT: &fakeSTT{text: "what's for dinner"}, Brain: o, VAD: &fakeVAD{}, Capture: newFakeCapture(), Events: events.NewBus(), CurrentSpeaker: func() string { return tc.mic }}
			events.Subscribe(p.Events, func(e events.UserInput) { shown = e.Speaker })
			if _, err := p.RunTurn(t.Context()); err != nil {
				t.Fatal(err)
			}
			if shown != tc.mic {
				t.Errorf("event speaker = %q, want %q", shown, tc.mic)
			}
			if history := o.History(); len(history) == 0 || history[0].Speaker != tc.mic {
				t.Errorf("stored history = %+v, want speaker %q", history, tc.mic)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) == 0 {
				t.Fatal("no /api/chat request")
			}
			for i, req := range requests {
				var last string
				for _, m := range req.Messages {
					if m.Role != "user" {
						continue
					}
					last = m.Content
					if strings.Contains(strings.ToLower(m.Content), "unknown") {
						t.Errorf("request %d names the unidentified label: %q", i, m.Content)
					}
				}
				if !strings.Contains(last, tc.wantUser) {
					t.Errorf("request %d last user = %q, want %q", i, last, tc.wantUser)
				}
				if tc.wantUser == "what's for dinner" && strings.Contains(last, ": what's for dinner") {
					t.Errorf("request %d attributes an unidentified turn: %q", i, last)
				}
			}
		})
	}
}
