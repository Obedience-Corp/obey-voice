//go:build integration

package brain

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

func TestLiveCurrentInformationRouting(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_LIVE_ROUTING_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_LIVE_ROUTING_MODEL to an installed local model")
	}
	candidates := []skills.Skill{
		{Name: "web-perf", Description: "Audit, diagnose, or optimize website loading and interaction performance, Core Web Vitals, and Lighthouse performance scores."},
		{Name: "cloudflare", Description: "Discover and choose Cloudflare products for apps, APIs, AI agents, storage, networking, and security. Use for architecture and product selection."},
		{Name: "turnstile-spin", Description: "Set up, repair, or migrate to Cloudflare Turnstile bot verification in an existing frontend and backend, including server-side Siteverify."},
	}
	base, _ := url.Parse("http://localhost:11434")
	selector := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: model, cfg: &config.Config{PromptsDir: t.TempDir()}}
	for _, tc := range []struct{ input, want string }{
		{"Uh Venessa, could you uh tell me the weather in Denver, Colorado today?", ""},
		{"Venessa, you look for the weather. You have access to the web.", ""},
		{"Audit this website's loading performance and Core Web Vitals", "web-perf"},
		{"Set up Cloudflare Turnstile bot verification in my app", "turnstile-spin"},
	} {
		matched, err := selector.confirmSkillMatches(t.Context(), tc.input, candidates)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, skill := range matched {
			if skill.Name == tc.want {
				found = true
			}
		}
		t.Logf("input=%q selected=%v", tc.input, matched)
		if tc.want == "" && len(matched) != 0 || tc.want != "" && !found {
			t.Fatalf("wrong skill selection for %q: %v", tc.input, matched)
		}
	}
	for _, tc := range []struct{ input, route string }{
		{"Uh Venessa, could you uh tell me the weather in Denver, Colorado today?", "spawn"},
		{"Venessa, you look for the weather. You have access to the web.", "spawn"},
		{"How are you doing today?", ""},
		{"Explain why rain forms.", ""},
	} {
		input := tc.input
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "prompts", "persona"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "prompts", "persona", "venessa.md"), []byte("You are Venessa. Answer conversationally and accurately in two sentences."), 0600); err != nil {
			t.Fatal(err)
		}
		b, err := NewOllama(&config.Config{OllamaHost: base.String(), OllamaModel: model, OllamaNumCtx: 8192, VoiceToolsEnabled: true, VoiceRouterTools: string(RouterToolsRouteOnly), AgentName: "Venessa", Persona: "venessa", WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), MaxHistory: 12})
		if err != nil {
			t.Fatal(err)
		}
		// Force the same false embedding shortlist seen in the user's capture;
		// the actual local model must reject it before answering/routing.
		router, err := newSemanticSkillRouter(t.Context(), &routingEmbedder{catalog: [][]float32{{1}, {1}, {1}}, queries: map[string][]float32{input: {1}}}, "fixture", .55, candidates, nil)
		if err != nil {
			t.Fatal(err)
		}
		b.skills, b.skillRouter = candidates, router
		activated := 0
		stream, err := b.ThinkStream(t.Context(), input, StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, OnToolStart: func(call ToolCall) {
			if call.Name == "activate_skill" {
				activated++
			}
		}})
		if err != nil {
			t.Fatal(err)
		}
		spoken, _ := drainStream(t, stream)
		result := <-stream.Done
		t.Logf("input=%q route=%q spoken=%q", input, result.Route, spoken)
		if result.Err != nil || result.Route != tc.route || activated != 0 {
			t.Fatalf("lookup was not routed: result=%+v spoken=%q", result, spoken)
		}
	}
}
