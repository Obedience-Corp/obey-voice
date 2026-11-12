package personaruntime

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

// writeClaudePersona saves a persona on Claude; an empty model leaves the
// choice to the CLI.
func writeClaudePersona(t *testing.T, c *claudeConversation, id, display, model string) {
	t.Helper()
	p := testProfile(id, display, id)
	p.Brain.Provider, p.Brain.Model = "claude", model
	writeTestProfile(t, c.catalog.Dir, p)
	writeTestPrompt(t, c.base.PromptsDir, id, "You are {agent_name}.")
}

func TestClaudePersonasEachAskForTheirOwnModel(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	writeClaudePersona(t, c, "mira", "Mira", "haiku")
	writeClaudePersona(t, c, "ava", "Ava", "opus")
	writeClaudePersona(t, c, "nova", "Nova", "")
	c.say("switch to Mira", "What do you think?")
	if got := c.selector.Snapshot().BrainModel; got != "haiku" {
		t.Fatalf("selection reports model %q, want haiku", got)
	}
	c.say("switch to Ava", "And you?", "switch to Nova", "Anything?", "switch to Mira", "Still there?")

	want := map[string]string{"Mira": "haiku", "Ava": "opus", "Nova": ""}
	seen := map[string]int{}
	for _, req := range readClaudeRequests(t, c.log) {
		model, ok := want[req.Persona]
		if !ok {
			t.Fatalf("request from an unexpected persona: %+v", req)
		}
		if req.Model != model {
			t.Fatalf("%s asked for model %q, want %q", req.Persona, req.Model, model)
		}
		seen[req.Persona]++
	}
	for name := range want {
		if seen[name] == 0 {
			t.Fatalf("%s never reached Claude; seen = %v", name, seen)
		}
	}
}

func TestEditedClaudeModelRetiresItsParkedSession(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	writeClaudePersona(t, c, "mira", "Mira", "haiku")
	c.say("switch to Mira", "What do you think?", "switch to Ember")
	if len(c.selector.parked) != 1 {
		t.Fatalf("parked = %+v", c.selector.parked)
	}
	mira := c.selector.parked[0].provider
	writeClaudePersona(t, c, "mira", "Mira", "sonnet")
	c.say("switch to Mira", "Still there?")
	if c.p.Brain == mira || len(c.selector.parked) != 0 {
		t.Fatalf("a persona with a new model reused its parked session; parked = %+v", c.selector.parked)
	}
	last := lastClaudeRequest(t, c.log)
	if last.Resume != "" || last.Model != "sonnet" {
		t.Fatalf("after the model change: %+v", last)
	}
	requireContains(t, last.Prompt, `"content":"What do you think?"`)
}

func TestEditedClaudeModelRestartsTheSpeakingSession(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newClaudeConversation(t, 20)
	c.say("Note the door is teal.", "And the window?")
	requests := readClaudeRequests(t, c.log)
	first, second := requests[0], requests[len(requests)-1]
	if first.Model != "" || second.Model != "" || second.Resume != first.Session {
		t.Fatalf("before the edit: %+v", requests)
	}
	writeClaudePersona(t, c, "mira", "Mira", "haiku")
	c.say("Still there?")
	last := lastClaudeRequest(t, c.log)
	if last.Resume != "" || last.Model != "haiku" {
		t.Fatalf("after the model change: %+v", last)
	}
	if c.p.Brain == c.initial {
		t.Fatal("the speaking persona kept its old provider after a model change")
	}
	requireContains(t, last.Prompt, `"content":"Note the door is teal."`)
}

func TestClaudeSessionSurvivesEditsThatDoNotChangeItsModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*persona.Profile)
	}{
		{"voice", func(p *persona.Profile) { p.TTS.Voice = "af_sky" }},
		{"empty model to default", func(p *persona.Profile) { p.Brain.Model = "default" }},
		{"padded default", func(p *persona.Profile) { p.Brain.Model = " Default " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
			c := newClaudeConversation(t, 20)
			c.say("Note the door is teal.")
			first := lastClaudeRequest(t, c.log)
			p := testProfile("mira", "Mira", "mira")
			p.Brain.Provider, p.Brain.Model = "claude", ""
			tc.edit(&p)
			writeTestProfile(t, c.catalog.Dir, p)
			c.say("Still there?")
			last := lastClaudeRequest(t, c.log)
			if c.p.Brain != c.initial || last.Resume != first.Session || last.Model != "" {
				t.Fatalf("the edit replaced the Claude session: first %+v, last %+v", first, last)
			}
		})
	}
}

func TestParkedClaudeSessionSurvivesAnEquivalentModelSpelling(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	writeClaudePersona(t, c, "mira", "Mira", "default")
	c.say("switch to Mira", "What do you think?", "switch to Ember")
	if len(c.selector.parked) != 1 {
		t.Fatalf("parked = %+v", c.selector.parked)
	}
	mira, session := c.selector.parked[0].provider, lastClaudeRequest(t, c.log).Session
	writeClaudePersona(t, c, "mira", "Mira", "")
	c.say("switch to Mira", "Still there?")
	if last := lastClaudeRequest(t, c.log); c.p.Brain != mira || last.Resume != session || last.Model != "" {
		t.Fatalf("an equivalent model spelling retired the parked session: %+v", last)
	}
}
