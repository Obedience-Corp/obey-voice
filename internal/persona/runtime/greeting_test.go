package personaruntime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestAddressedGreetingPreservesSharedHistoryWithoutInference(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "group enabled"}[group], func(t *testing.T) {
			first := testProfile("marigold-19", "Marigold", "marigold")
			second := testProfile("juniper-42", "Juniper", "juniper")
			second.Aliases = []string{"Moss"}
			f := newSelectorTestFixture(t, first, second)
			defer f.close(t)
			f.selector.opts.Base.GroupParticipationEnabled = group
			f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
				t.Fatal("a directly addressed standalone greeting invoked model classification")
				return brain.Addressee{}, nil
			})
			before := []brain.Turn{
				{Role: "user", Content: "Inspect the newest planning item."},
				{Role: "assistant", Content: "I found amber-circuit and reported its findings.", PersonaID: first.ID, PersonaDisplayName: first.DisplayName},
				{Role: "user", Content: "Prepare a structured proposal for it."},
			}
			f.old.LoadHistory(before)
			f.pipeline.Events = events.NewBus()
			f.pipeline.SetOutputMuted(true)
			f.pipeline.AdmitInput = f.selector.Route
			f.pipeline.OnTurn = func() {
				if err := f.store.Save(f.session, f.pipeline.Brain.History(), f.selector.ContextSnapshots()); err != nil {
					t.Error(err)
				}
			}
			var reply events.ResponseReady
			events.Subscribe(f.pipeline.Events, func(e events.ResponseReady) { reply = e })
			const input = "Moss. How’s it going today?"
			if err := f.pipeline.RunTurnTextMode(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if reply.Response != "Juniper here. How can I help?" || reply.Envelope == nil || reply.Envelope.PersonaID != second.ID {
				t.Fatalf("greeting lost configured persona: %+v", reply)
			}
			f.factoryMu.Lock()
			candidate := f.factories[0]
			f.factoryMu.Unlock()
			if f.old.ThinkCount() != 0 || candidate.ThinkCount() != 0 {
				t.Fatal("greeting resumed old assignments through a model")
			}
			loaded, err := f.store.Load(f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Turns) != len(before)+2 || !reflect.DeepEqual(loaded.Turns[:len(before)], before) ||
				loaded.Turns[len(before)].Content != input || loaded.Turns[len(before)+1].PersonaID != second.ID || loaded.Turns[len(before)+1].Content != reply.Response {
				t.Fatalf("selection replayed or changed history: %+v", loaded.Turns)
			}
		})
	}
}

func TestAddressedGreetingDoesNotConsumeSubstantiveRequests(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("juniper-42", "Juniper", "juniper"))
	defer f.close(t)
	for _, input := range []string{
		"Juniper, how's the inspection going?",
		"Juniper, hello, please inspect the newest item.",
		"Juniper, say hello to the team.",
		"Juniper, what do you think of the findings?",
		"Juniper, repeat the earlier answer.",
		"Juniper, how are you today and can you check the file?",
	} {
		admitted, err := f.selector.Route(t.Context(), input, false)
		if err != nil {
			t.Fatal(err)
		}
		if admitted.LocalReply != "" || !strings.Contains(admitted.Request, strings.TrimPrefix(input, "Juniper, ")) {
			t.Fatalf("substantive request consumed as greeting: input=%q admission=%+v", input, admitted)
		}
	}
}

func TestGreetingLocalControlOnlyUsesConfiguredRoster(t *testing.T) {
	f := newSelectorTestFixture(t, persona.Profile{
		Schema: persona.Schema, ID: "custom", DisplayName: "River", Aliases: []string{"Riv"},
		Brain:   persona.Brain{Provider: "ollama", Model: "fixture"},
		Prompts: persona.PromptRefs{Persona: "custom"},
	})
	defer f.close(t)
	for _, input := range []string{"River, hello!", "Riv, good evening.", "River. How's it going today?", "@custom how are you today?"} {
		admitted, err := f.selector.Route(t.Context(), input, false)
		if err != nil || admitted.Envelope.PersonaID != "custom" || admitted.LocalReply != "River here. How can I help?" {
			t.Fatalf("configured name/alias did not use the same greeting path: %q %+v %v", input, admitted, err)
		}
	}
	admitted, err := f.selector.Route(t.Context(), "hello!", false)
	if err != nil || admitted.LocalReply != "" {
		t.Fatalf("unnamed input unexpectedly became a persona-selection control: %+v %v", admitted, err)
	}
}

func TestAddressedGreetingUsesEditablePromptAndBrokenOverrideFallback(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("custom", "River", "custom"))
	defer f.close(t)
	dir := filepath.Join(f.selector.Config().PromptsDir, "system")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "persona-greeting.yaml")
	for _, tc := range []struct{ doc, want string }{
		{"schema: obey-voice.prompt.v1\nprompt:\n  name: persona-greeting\n  kind: system\n  system_prompt: '{agent_name} is ready when you are.'\n", "River is ready when you are."},
		{"not: [valid YAML", "River here. How can I help?"},
	} {
		if err := os.WriteFile(path, []byte(tc.doc), 0600); err != nil {
			t.Fatal(err)
		}
		admitted, err := f.selector.Route(t.Context(), "@custom hello", false)
		if err != nil || admitted.LocalReply != tc.want {
			t.Fatalf("greeting override/fallback: %+v %v", admitted, err)
		}
	}
}
