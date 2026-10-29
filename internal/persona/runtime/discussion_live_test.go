package personaruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type observedDecider struct {
	discussion.Decider
	checks int
}

func (d *observedDecider) Check(ctx context.Context, c discussion.Context, p discussion.Persona) (discussion.Contribution, error) {
	d.checks++
	return d.Decider.Check(ctx, c, p)
}

// Opt-in uses real private inference, persona preparation, streaming and session persistence.
// No microphone, TTS, personal config, worker tools or model downloads.
func TestLiveGroupDiscussionPipeline(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_LIVE_DISCUSSION_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_LIVE_DISCUSSION_MODEL to an installed local model")
	}
	f, _, _ := groupFixture(t)
	for _, id := range []string{"jarvis", "samantha"} {
		profile, err := f.catalog.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		profile.Brain.Model = model
		writeTestProfile(t, f.catalog.Dir, *profile)
	}
	target, _ := url.Parse("http://127.0.0.1:11434")
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport
	proxy.Transport = discussionCaptureTransport{base: transport, t: t}
	server := httptest.NewServer(proxy)
	defer server.Close()
	base := f.selector.opts.Base.Clone()
	base.OllamaHost = server.URL
	base.OllamaModel = model
	base.MaxHistory = 20
	base.OllamaNumCtx = 8192
	base.OllamaNumBatch = 128
	base.SkillsEnabled = false
	base.GroupParticipationEnabled = true
	base.PersonaRoutingTimeout = 30
	base.ContinuityIntegrityEnabled = true
	f.selector.opts.Base = base
	binding, err := f.catalog.ResolveBinding(base, "jarvis")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := brain.NewOllama(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.ImportContinuity(f.old.ContinuitySnapshot()); err != nil {
		t.Fatal(err)
	}
	f.pipeline.Brain = initial
	f.selector.opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) { return brain.NewOllama(cfg) }
	f.selector.opts.NewWorker = func(context.Context, *config.Config) (brain.Provider, error) { return nil, nil }
	private, err := initial.DiscussionDecider()
	if err != nil {
		t.Fatal(err)
	}
	observer := &observedDecider{Decider: private}
	f.selector.opts.DiscussionDecider = observer
	responses := []events.ResponseReady{}
	events.Subscribe(f.pipeline.Events, func(e events.ResponseReady) { responses = append(responses, e) })
	events.Subscribe(f.pipeline.Events, func(e events.Info) { t.Log(e.Message) })
	input := "Jarvis and Samantha, discuss whether a local prototype should precede adding a separate deployment service. Give your own useful view, and ask each other a question if there is a concrete tradeoff left to resolve."
	if err := f.pipeline.RunTurnTextMode(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if observer.checks < 2 {
		t.Fatalf("only %d invited opportunities", observer.checks)
	}
	if len(responses) == 0 {
		t.Fatal("no actual model contribution")
	}
	for _, r := range responses {
		if r.Degraded || r.Interrupted {
			t.Fatalf("incomplete response: %+v", r)
		}
		t.Logf("%s: %s", r.Envelope.DisplayName, r.Response)
	}
	saved, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	humans := 0
	for _, turn := range saved.Turns {
		if turn.Role == "user" && turn.Content == input {
			humans++
		}
		if turn.Role == "tool" || len(turn.ToolCalls) > 0 {
			t.Fatal("group conversation executed tools")
		}
	}
	if humans != 1 {
		t.Fatalf("persisted user requests=%d", humans)
	}
	t.Logf("Real contributions=%d; private opportunities=%d; persisted turns=%d", len(responses), observer.checks, len(saved.Turns))
}

// Only the synthetic fixture's requests pass through this transport.
type discussionCaptureTransport struct {
	base http.RoundTripper
	t    *testing.T
}

func (d discussionCaptureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/api/chat" && r.Body != nil {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct {
			Messages []struct {
				Role    string
				Content string
			}
			Format json.RawMessage
		}
		_ = json.Unmarshal(raw, &body)
		if len(body.Format) == 0 {
			for _, m := range body.Messages {
				d.t.Logf("wire %s: %.120s", m.Role, m.Content)
			}
		}
	}
	return d.base.RoundTrip(r)
}
