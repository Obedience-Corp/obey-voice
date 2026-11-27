package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// runSummon shows name summon from outside the repository: an agent called
// Juno that answers only what is said to it, the way a single-purpose voice
// assistant would. Everything is injected, so it runs with no microphone, no
// models and no home directory, and it fails if the gate lets the wrong thing
// through.
//
// A real host drops the injected STT, TextOnly and Silent, sets
// voice_frontend, and calls agent.RunTurn in a loop.
func runSummon() error {
	heard := &scriptedSTT{}
	answered := &echoBrain{}
	bus := events.NewBus()
	var dropped []string
	events.Subscribe(bus, func(e events.Info) { dropped = append(dropped, e.Message) })

	agent, cleanup, err := voiceagent.New(context.Background(), voiceagent.Options{
		Config: &config.Config{
			AgentName:         "Juno",
			NameSummonEnabled: true,
			NameSummonAliases: []string{"computer"},
		},
		Events: bus,
		Brain:  answered,
		STT:    heard,
		// A nameless reply within ten seconds of the agent's answer counts.
		NameSummonFollowUp: 10 * time.Second,
		TextOnly:           true,
		Silent:             true,
	})
	if err != nil {
		return fmt.Errorf("new agent: %w", err)
	}
	defer cleanup()
	fmt.Println("wake words:", strings.Join(agent.NameSummon().Names(), ", "))

	steps := []struct {
		say    string
		answer bool
		why    string
	}{
		{"what's for dinner tonight?", false, "talk in the room, not for the agent"},
		{"I told Juno earlier", false, "mentions the name, does not open with it"},
		{"Hey Juno, what time is it?", true, "opens with the name"},
		{"and tomorrow?", true, "follow-up right after a reply"},
		{"Computer, lights off.", true, "opens with an alias"},
	}
	for _, step := range steps {
		before := answered.calls
		heard.next = step.say
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		_, err := agent.RunTurn(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("turn %q: %w", step.say, err)
		}
		got := answered.calls > before
		fmt.Printf("%-30q answered=%-5v (%s)\n", step.say, got, step.why)
		if got != step.answer {
			return fmt.Errorf("%q: answered=%v, want %v", step.say, got, step.answer)
		}
	}
	if len(dropped) == 0 || !strings.Contains(dropped[0], "Not addressed to Juno") {
		return fmt.Errorf("the dropped utterance was not reported on the bus: %v", dropped)
	}
	fmt.Println("ok: name summon gated voice turns for an outside module")
	return nil
}

// scriptedSTT stands in for a recogniser: one finished transcript per turn.
type scriptedSTT struct{ next string }

func (s *scriptedSTT) Available() bool { return true }
func (s *scriptedSTT) Start(context.Context) (stt.Session, error) {
	ch := make(chan stt.Event, 1)
	ch <- stt.FinalTranscript{Text: s.next}
	close(ch)
	return scriptedSession(ch), nil
}

type scriptedSession chan stt.Event

func (s scriptedSession) Events() <-chan stt.Event { return s }
func (s scriptedSession) Close() error             { return nil }
