package personaruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestCodexSelectorInitialSwitchReloadAndRestore(t *testing.T) {
	log := installCodexSelectorFixture(t)
	root := t.TempDir()
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	promptDir := filepath.Join(root, "prompts")
	for _, id := range []string{"santa", "samantha"} {
		profile := testProfile(id, map[string]string{"santa": "Santa", "samantha": "Samantha"}[id], id)
		profile.Brain.Provider = "codex"
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
	}
	base := &config.Config{ActivePersona: "santa", BrainProvider: "codex", PromptsDir: promptDir, WorkDir: root, MaxHistory: 20, EnvironmentContextEnabled: true}
	binding, err := catalog.ResolveBinding(base, "santa")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := brain.NewCodex(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: provider, Events: events.NewBus()}
	t.Cleanup(func() {
		if err := p.CloseBrains(); err != nil {
			t.Error(err)
		}
	})
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("codex", "fixture")
	selector, err := NewSelector(SelectionOptions{Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: binding,
		Persist: func(turns []brain.Turn, selection events.PersonaSelection) error {
			return store.SaveSelection(sess, turns, selection.PersonaID, selection.DisplayName, selection.PromptHash)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.AdmitInput = selector.Route
	p.OnTurn = func() {
		if err := store.Save(sess, p.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	run := func(input string) {
		t.Helper()
		if err := p.RunTurnTextMode(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	initial := selector.Snapshot()
	run("Remember cobalt.")
	if p.Brain != provider || selector.Snapshot().Revision != initial.Revision {
		t.Fatal("first turn replaced the selected persona instead of using it")
	}
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Turns) != 2 || saved.Turns[1].PersonaID != "santa" || saved.Turns[1].PromptHash != initial.PromptHash || saved.Turns[1].ResponseID == "" {
		t.Fatalf("startup response identity not persisted: %+v", saved)
	}
	// Model context may be compacted while canonical history stays complete.
	snapshot := provider.ContinuitySnapshot()
	snapshot.Working = []brain.Turn{{Role: "assistant", Content: "Shared summary: Santa remembers cobalt."}}
	snapshot.Archive = []continuity.Entry{{Summary: "archived fact"}}
	snapshot.ContextHighWater = 8192
	if err := provider.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := selector.Select(ctx, "samantha", "explicit"); err != nil {
		t.Fatal(err)
	}
	sharedSnapshot := brain.ContinuitySnapshot{Canonical: snapshot.Canonical, Working: snapshot.Canonical}
	assertContinuity := func() {
		t.Helper()
		got := p.Brain.(brain.ContinuityTransferProvider).ContinuitySnapshot()
		if !reflect.DeepEqual(got, sharedSnapshot) {
			t.Fatalf("selection lost continuity: got=%+v want=%+v", got, sharedSnapshot)
		}
	}
	assertContinuity()
	nextBase := base.Clone()
	nextBase.CodexReasoningEffort = "high"
	if err := selector.Reload(ctx, nextBase); err != nil {
		t.Fatal(err)
	}
	assertContinuity()
	writeTestPrompt(t, promptDir, "samantha", "You are {agent_name}. Use concise replies.")
	run("Recall the code.")
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "samantha" || len(loaded.Turns) != 4 || loaded.Turns[3].PersonaID != "samantha" || loaded.Turns[3].PromptHash != selector.Snapshot().PromptHash {
		t.Fatalf("switch/reload identity not persisted: %+v", loaded)
	}
	p.Brain.LoadHistory(loaded.Turns)
	selector.RestoreSession(sess.ID, loaded.SelectedPersonaID, loaded.SelectedPersonaDisplayName, loaded.SelectedPersonaPromptHash)
	if _, err := selector.Select(ctx, loaded.SelectedPersonaID, "restore"); err != nil {
		t.Fatal(err)
	}
	run("Recall it again.")
	restored, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Turns) != 6 || restored.Turns[5].PersonaID != "samantha" {
		t.Fatalf("restored turn rejected or lost identity: %+v", restored)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var prompts, systems []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m struct {
			Method string
			Params struct {
				DeveloperInstructions string
				Input                 []struct{ Text string }
			}
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		if m.Method == "thread/start" {
			systems = append(systems, m.Params.DeveloperInstructions)
		}
		if m.Method == "turn/start" {
			prompts = append(prompts, m.Params.Input[0].Text)
		}
	}
	if len(prompts) != 3 || len(systems) != 3 {
		t.Fatalf("unexpected model calls: prompts=%v systems=%v", prompts, systems)
	}
	if !strings.Contains(systems[0], "You are Santa.") || !strings.Contains(systems[1], "You are Samantha. Use concise replies.") || strings.Contains(prompts[1], "Shared summary: Santa remembers cobalt.") || !strings.Contains(prompts[1], `"persona":"Santa"`) || !strings.Contains(prompts[2], `"persona":"Santa"`) || !strings.Contains(prompts[2], `"persona":"Samantha"`) {
		t.Fatalf("wire lost current persona or historical context: systems=%v prompts=%v", systems, prompts)
	}
}
