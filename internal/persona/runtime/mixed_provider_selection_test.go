package personaruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

const (
	emberReply = "Ember says the vault code is cobalt."
	scoutReply = "Scout says the vault door is teal."
)

// installMixedProviderBinaries puts fake muse and obey executables on PATH. Both
// re-run this test binary, record the prompt they receive, and return a fixed reply.
func installMixedProviderBinaries(t *testing.T) string {
	t.Helper()
	binary := fixtureBinary(t)
	scripts := map[string]string{}
	for _, name := range []string{"muse", "obey"} {
		scripts[name] = "#!/bin/sh\nOBEY_MIXED_FIXTURE=" + name + " exec '" + binary + "' -test.run='^TestMixedProviderFixtureProcess$' -- \"$@\"\n"
	}
	dir := fixtureScriptDir(t, "mixed", scripts)
	log := filepath.Join(t.TempDir(), "prompts.jsonl")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OBEY_MIXED_FIXTURE_LOG", log)
	return log
}

func TestMixedProviderFixtureProcess(t *testing.T) {
	kind, log := os.Getenv("OBEY_MIXED_FIXTURE"), os.Getenv("OBEY_MIXED_FIXTURE_LOG")
	if kind == "" || log == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	record := func(prompt string) {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		line, _ := json.Marshal(map[string]string{"provider": kind, "prompt": prompt})
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
	out := json.NewEncoder(os.Stdout)
	// A request asking to stall answers in part, or not at all, and waits to be
	// stopped. Conversation records quoting an earlier request are JSON lines.
	stalls := func(prompt string) bool {
		for line := range strings.Lines(prompt) {
			if !strings.HasPrefix(line, "{") && strings.Contains(line, claudeStall) {
				return true
			}
		}
		return false
	}
	switch kind {
	case "muse":
		record(args[len(args)-1])
		if stalls(args[len(args)-1]) {
			_ = out.Encode(map[string]any{"payload_type": "run.output.delta", "payload": map[string]string{"text": claudePartial}})
			time.Sleep(time.Minute)
			os.Exit(0)
		}
		_ = out.Encode(map[string]any{"payload_type": "run.output.delta", "payload": map[string]string{"text": scoutReply}})
		_ = out.Encode(map[string]any{"payload_type": "run.terminal.completed", "payload": map[string]string{"terminal": "completed", "text": scoutReply}})
	case "obey":
		if len(args) > 0 && args[0] == "--socket" {
			args = args[2:]
		}
		if len(args) > 0 && args[0] == "doctor" {
			_, _ = os.Stdout.WriteString(`{"schema":"obey.doctor","version":3,"status":"ok","capabilities":{"session_run":{"protocol":"obey.session.run","versions":[1]}}}` + "\n")
			os.Exit(0)
		}
		stdin, _ := io.ReadAll(bufio.NewReader(os.Stdin))
		record(string(stdin))
		if stalls(string(stdin)) {
			time.Sleep(time.Minute)
			os.Exit(0)
		}
		emit := func(kind string, fields map[string]any) {
			fields["protocol"], fields["version"], fields["type"] = "obey.session.run", 1, kind
			_ = out.Encode(fields)
		}
		emit("session_created", map[string]any{"session_id": "s1", "campaign_id": "c1"})
		emit("assistant_content", map[string]any{"session_id": "s1", "content": scoutReply, "final": true})
		emit("usage", map[string]any{"session_id": "s1", "input_tokens": 3, "output_tokens": 2, "cached_tokens": 0, "latency_ms": 1})
		emit("terminal", map[string]any{"session_id": "s1", "status": "completed"})
	}
	os.Exit(0)
}

type fixturePrompt struct{ Provider, Prompt string }

func readFixturePrompts(t *testing.T, log string) []fixturePrompt {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out []fixturePrompt
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var p fixturePrompt
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestSelectorSharesConversationBetweenOllamaAndCLIProviders(t *testing.T) {
	for _, tc := range []struct{ provider, sharing string }{
		{"muse-spark", ""}, {"obey", ""}, {"muse-spark", "whole_conversation"}, {"obey", "whole_conversation"},
	} {
		provider, whole := tc.provider, tc.sharing == "whole_conversation"
		t.Run(provider+"/"+tc.sharing, func(t *testing.T) {
			log := installMixedProviderBinaries(t)
			root, _ := filepath.EvalSymlinks(t.TempDir())
			var mu sync.Mutex
			var ollamaRequests [][]struct{ Role, Content string }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/api/chat" {
					_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
					return
				}
				var req struct {
					Messages []struct{ Role, Content string }
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				ollamaRequests = append(ollamaRequests, req.Messages)
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "message": map[string]string{"role": "assistant", "content": emberReply}, "done": true, "done_reason": "stop"})
			}))
			defer server.Close()

			catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
			promptDir := filepath.Join(root, "prompts")
			for id, display := range map[string]string{"ember": "Ember", "scout": "Scout"} {
				profile := testProfile(id, display, id)
				profile.Brain.Model = ""
				if id == "scout" {
					profile.Brain.Provider = provider
				}
				writeTestProfile(t, catalog.Dir, profile)
				writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
			}
			base := &config.Config{ActivePersona: "ember", BrainProvider: "ollama", OllamaModel: "fixture:latest", OllamaHost: server.URL,
				ObeyProvider: "fixture", ObeyModel: "fixture", ObeyToolPolicy: "daemon", PromptsDir: promptDir, WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192, HostedPersonaSharing: tc.sharing}
			binding, err := catalog.ResolveBinding(base, "ember")
			if err != nil {
				t.Fatal(err)
			}
			initial, err := brain.NewOllama(binding.Config())
			if err != nil {
				t.Fatal(err)
			}
			p := &pipeline.Pipeline{Brain: initial, Events: events.NewBus()}
			t.Cleanup(func() {
				if err := p.CloseBrains(); err != nil {
					t.Error(err)
				}
			})
			store := session.NewStore(filepath.Join(root, "sessions"))
			sess := session.New("ollama", "fixture:latest")
			selector, err := NewSelector(SelectionOptions{Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: binding,
				Persist: func(turns []brain.Turn, s events.PersonaSelection) error {
					return store.SaveSelection(sess, turns, s.PersonaID, s.DisplayName, s.PromptHash, s.Directories)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			p.AdmitInput = selector.Route
			p.HostedNotice = selector.HostedNotice
			var notices []string
			events.Subscribe(p.Events, func(e events.Info) { notices = append(notices, e.Message) })
			p.OnTurn = func() {
				if err := store.Save(sess, p.Brain.History()); err != nil {
					t.Error(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			for _, input := range []string{"Note that the vault code is cobalt.", "switch to Scout", "What color is the door?", "switch to Ember", "What did we say about the door?"} {
				if err := p.RunTurnTextMode(ctx, input); err != nil {
					t.Fatalf("%q: %v", input, err)
				}
			}

			loaded, err := store.Load(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			var replies []brain.Turn
			for _, turn := range loaded.Turns {
				if turn.Role == "assistant" {
					replies = append(replies, turn)
				}
			}
			wantIDs := []string{"ember", "scout", "scout", "ember", "ember"}
			wantText := []string{emberReply, "Scout here.", scoutReply, "Ember here.", emberReply}
			if len(replies) != len(wantIDs) {
				t.Fatalf("stored replies = %+v", loaded.Turns)
			}
			for i, turn := range replies {
				if turn.PersonaID != wantIDs[i] || turn.Content != wantText[i] || turn.ResponseID == "" || turn.PromptHash == "" {
					t.Fatalf("reply %d stored as %+v, want persona %q text %q", i, turn, wantIDs[i], wantText[i])
				}
			}

			label := map[string]string{"muse-spark": "Muse Spark", "obey": "Obey"}[provider]
			want := "Scout runs on " + label + ", a hosted service. Only what is said from when it was invited is sent to " + label + ", plus jobs it started earlier and their results; earlier lines stay on this machine."
			if whole {
				want = "Scout runs on " + label + ", a hosted service. The whole conversation is sent to " + label + "."
			}
			if len(notices) != 1 || notices[0] != want {
				t.Fatalf("notices = %q, want %q", notices, want)
			}
			prompts := readFixturePrompts(t, log)
			if len(prompts) != 1 || prompts[0].Provider != map[string]string{"muse-spark": "muse", "obey": "obey"}[provider] {
				t.Fatalf("%s calls = %+v", provider, prompts)
			}
			scoutPrompt := prompts[0].Prompt
			requireContains(t, scoutPrompt, "You are Scout.", `"content":"switch to Scout"`, `"content":"Scout here.","persona":"Scout"`, "What color is the door?")
			requireOmits(t, scoutPrompt, "You are Ember.")
			// Ember spoke before Scout was invited.
			if whole {
				requireContains(t, scoutPrompt, "Note that the vault code is cobalt.", `"content":"`+emberReply+`","persona":"Ember"`)
			} else {
				requireOmits(t, scoutPrompt, "vault code", emberReply, `"persona":"Ember"`)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(ollamaRequests) != 2 {
				t.Fatalf("ollama requests = %d", len(ollamaRequests))
			}
			var returned strings.Builder
			for _, m := range ollamaRequests[1] {
				returned.WriteString(m.Role + ": " + m.Content + "\n")
				if m.Role == "assistant" && strings.Contains(m.Content, scoutReply) {
					t.Fatalf("Scout's statement was replayed as Ember's own:\n%s", returned.String())
				}
			}
			if !strings.Contains(ollamaRequests[1][0].Content, "You are Ember.") || !strings.Contains(returned.String(), scoutReply) || !strings.Contains(returned.String(), "What color is the door?") {
				t.Fatalf("returning Ember lacks what was said while away:\n%s", returned.String())
			}
		})
	}
}

// Every registered provider shares a conversation by one of the verified
// projections; any other is refused rather than silently replaced.
func TestSelectorRefusesUnverifiedProvider(t *testing.T) {
	const provider = "fixture-unverified"
	scout := testProfile("scout", "Scout", "scout")
	scout.Brain.Provider = provider
	f := newSelectorTestFixture(t, testProfile("ember", "Ember", "ember"), scout)
	defer f.close(t)
	_, err := f.selector.Select(t.Context(), "scout", "explicit")
	want := `persona "scout": provider "` + provider + `" has no verified shared history projection`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if got := f.selector.Snapshot().PersonaID; got != "ember" {
		t.Fatalf("selection moved to %q", got)
	}
}
