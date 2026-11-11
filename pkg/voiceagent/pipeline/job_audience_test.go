package pipeline

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// claudePipeline is a pipeline whose live provider keeps a Claude session,
// with live as the selected persona's configuration and turns as the
// conversation so far. A nil live leaves the pipeline without an Agency.
func claudePipeline(t *testing.T, live *config.Config, turns ...brain.Turn) *Pipeline {
	t.Helper()
	p := &Pipeline{Brain: &brain.Brain{}, Transcript: &recordedTranscript{turns: turns}}
	if live != nil {
		jobs := job.NewTable(t.Context())
		t.Cleanup(jobs.Close)
		NewAgency(p, jobs, nil, live, nil).Wire()
	}
	return p
}

func resultFrom(admission map[string]string) work.ResultView {
	return work.ResultView{State: work.Pending, Outcome: work.Outcome{Config: admission}}
}

func reply(personaID string) brain.Turn {
	return brain.Turn{Role: "assistant", Content: "Reply.", PersonaID: personaID}
}

func TestDeliverableResultOnAPersonaKeepingItsOwnSession(t *testing.T) {
	unset := &config.Config{BrainProvider: "claude"}
	mira := &config.Config{BrainProvider: "claude", ActivePersona: "mira"}
	cases := []struct {
		name      string
		live      *config.Config
		turns     []brain.Turn
		admission map[string]string
		want      bool
	}{
		{"Claude only with active_persona unset", unset, nil, frozenConfig(unset), true},
		{"unset active_persona against the default persona", unset, []brain.Turn{reply(persona.DefaultID)}, map[string]string{"persona": persona.DefaultID}, true},
		{"default persona against unset active_persona", &config.Config{ActivePersona: persona.DefaultID}, nil, map[string]string{"persona": ""}, true},
		{"result journaled before persona tagging", mira, []brain.Turn{reply("mira")}, map[string]string{"provider": "ollama"}, true},
		{"no Agency", nil, []brain.Turn{reply("mira")}, map[string]string{"persona": "mira"}, true},
		{"no Agency and no persona key", nil, nil, nil, true},
		{"job of a persona that never replied", mira, []brain.Turn{reply("mira")}, map[string]string{"persona": "ember"}, false},
		{"its own job with another persona present", mira, []brain.Turn{reply("mira"), reply("ember")}, map[string]string{"persona": "mira"}, true},
		{"another persona's job", mira, []brain.Turn{reply("mira"), reply("ember")}, map[string]string{"persona": "ember"}, false},
		{"untagged job with another persona present", mira, []brain.Turn{reply("mira"), reply("ember")}, map[string]string{}, false},
		{"another persona's job before the live one replied", mira, []brain.Turn{reply("ember")}, map[string]string{"persona": "ember"}, false},
		{"no Agency with two personas present", nil, []brain.Turn{reply("mira"), reply("ember")}, map[string]string{"persona": "ember"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := claudePipeline(t, tc.live, tc.turns...)
			if got := p.deliverableResult(resultFrom(tc.admission)); got != tc.want {
				t.Fatalf("deliverableResult = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeliverableResultLeavesSharedHistoryProvidersAlone(t *testing.T) {
	p := &Pipeline{Brain: &fakeBrain{}, Transcript: &recordedTranscript{turns: []brain.Turn{reply("mira"), reply("ember")}}}
	if !p.deliverableResult(resultFrom(map[string]string{"persona": "ember"})) {
		t.Fatal("a provider without its own session withheld a result")
	}
}

func TestClaudeOnlyConversationDeliversItsPendingResult(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	p.Brain, p.Transcript = &brain.Brain{}, &recordedTranscript{}
	if !p.BackgroundPending() {
		t.Fatalf("a Claude-only conversation with active_persona unset never offers its result: %+v", c.Pending(scope))
	}
	selected := 0
	for _, r := range c.Pending(scope) {
		if (r.State == work.Pending || r.State == work.Ready) && p.deliverableResult(r) {
			selected++
		}
	}
	if selected != 1 {
		t.Fatalf("deliverable results = %d", selected)
	}
}
