package brain

import (
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
)

func TestCodexFrozenPersonaMatchesSelectionHash(t *testing.T) {
	c, _ := codexFixture(t)
	c.cfg = &config.Config{AgentName: "Helper", WorkDir: t.TempDir(), EnvironmentContextEnabled: true}
	persona, err := personaSystemPrompt(c.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.personaPrompt = persona
	c.systemPrompt = c.assembleSystem(persona)
	if c.systemPrompt == persona {
		t.Fatal("fixture omitted assembled environment context")
	}
	want, err := PersonaPromptHash(c.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.FreezePersonaPrompt() != want || c.Fresh().(PersonaPromptFreezer).FreezePersonaPrompt() != want {
		t.Fatal("frozen live/worker identity does not match persona document hash")
	}
}

func TestCodexImportHistoryPreservesContinuityMetadata(t *testing.T) {
	c, f := codexFixture(t)
	snapshot := ContinuitySnapshot{Archive: []continuity.Entry{{Summary: "archived fact"}}, ContextHighWater: 8192}
	if err := c.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	c.sessionID = "old-thread"
	turns := []Turn{{Role: "user", Content: "shared question"}, {Role: "assistant", Content: "shared reply", PersonaID: "other"}}
	if err := c.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	got := c.ContinuitySnapshot()
	if !reflect.DeepEqual(got.Archive, snapshot.Archive) || got.ContextHighWater != snapshot.ContextHighWater || !reflect.DeepEqual(got.Canonical, turns) || !reflect.DeepEqual(got.Working, turns) {
		t.Fatalf("projection reset continuity: %+v", got)
	}
	turns[0].Content = "mutated caller"
	if _, err := c.ThinkFull(t.Context(), "continue", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].ThreadID != "" || c.History()[0].Content != "shared question" {
		t.Fatal("projection reused stale thread or aliased transcript")
	}
	c.LoadHistory(nil)
	got = c.ContinuitySnapshot()
	if len(got.Archive) != 0 || got.ContextHighWater != 0 {
		t.Fatal("explicit session restore retained other session's metadata")
	}
}
