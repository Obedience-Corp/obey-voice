package voiceagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

const unaddressed = "what's for dinner tonight?"

// summonOptions is a host that hears speech without a microphone: text mode
// with an injected recogniser, which is the cheapest arrangement name summon
// applies to.
func summonOptions(cfg *config.Config) Options {
	return Options{Config: cfg, Events: events.NewBus(), Brain: &stubBrain{}, TextOnly: true, Silent: true, STT: &stubSTT{}}
}

func TestNewInstallsNameSummonFromConfig(t *testing.T) {
	agent, cleanup, err := New(context.Background(), summonOptions(&config.Config{
		AgentName: "Juno", NameSummonEnabled: true, NameSummonAliases: []string{"computer"},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	if agent.NameSummon() == nil || agent.AddressGate == nil {
		t.Fatal("name_summon_enabled was set but no gate reached the pipeline")
	}
	if agent.AddressGate(unaddressed) == "" {
		t.Error("the installed gate answers to anything")
	}
	if reason := agent.AddressGate("Computer, lights off"); reason != "" {
		t.Errorf("an alias was vetoed: %q", reason)
	}
	agent.NameSummon().SetBypass(true)
	if reason := agent.AddressGate(unaddressed); reason != "" {
		t.Errorf("the bypass did not reach the installed gate: %q", reason)
	}
}

func TestNewInstallsNoNameSummonGate(t *testing.T) {
	on := config.Config{AgentName: "Juno", NameSummonEnabled: true}
	off := config.Config{AgentName: "Juno"}
	external := summonOptions(&on)
	external.ExternalAddressing = true
	typedOnly := summonOptions(&on)
	typedOnly.STT = nil

	for name, opts := range map[string]Options{
		"name summon off":                  summonOptions(&off),
		"the host owns addressing":         external,
		"no voice input to gate":           typedOnly,
		"the host owns addressing, no mic": {Config: &on, Events: events.NewBus(), Brain: &stubBrain{}, TextOnly: true, Silent: true, ExternalAddressing: true},
	} {
		t.Run(name, func(t *testing.T) {
			agent, cleanup, err := New(context.Background(), opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer cleanup()
			if agent.NameSummon() != nil || agent.AddressGate != nil {
				t.Fatal("a gate was installed")
			}
		})
	}
}

// With its own microphone and its own playback and nothing cancelling echo
// between them, the agent would summon itself. New must say so before it
// opens a device, not start an agent that answers its own voice.
func TestNewRefusesNameSummonThatWouldSelfTrigger(t *testing.T) {
	_, _, err := New(context.Background(), Options{
		Config: &config.Config{AgentName: "Juno", NameSummonEnabled: true, VoiceFrontend: config.VoiceFrontendOff},
		Events: events.NewBus(), Brain: &stubBrain{}, TTS: &stubTTS{}, Player: &stubPlayer{},
	})
	if err == nil || !strings.Contains(err.Error(), "voice_frontend") {
		t.Fatalf("err = %v, want a refusal naming voice_frontend", err)
	}
}

// A host that injects capture owns the audio path, and whatever cancels echo
// on it. New cannot see that, so it must not refuse on the config alone.
func TestNewTrustsAHostThatOwnsCaptureWithNameSummon(t *testing.T) {
	agent, cleanup, err := New(context.Background(), Options{
		Config: &config.Config{AgentName: "Juno", NameSummonEnabled: true, VoiceFrontend: config.VoiceFrontendOff},
		Events: events.NewBus(), Brain: &stubBrain{}, TTS: &stubTTS{}, Player: &stubPlayer{},
		Capture: &stubCapture{}, STT: &stubSTT{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()
	if agent.NameSummon() == nil {
		t.Fatal("no gate was installed")
	}
}

func TestNewRefusesNameSummonWithNoName(t *testing.T) {
	_, _, err := New(context.Background(), summonOptions(&config.Config{NameSummonEnabled: true}))
	if err == nil || !strings.Contains(err.Error(), "agent_name") {
		t.Fatalf("err = %v, want a refusal naming agent_name", err)
	}
}

func TestNewRejectsAFollowUpWindowThatCannotApply(t *testing.T) {
	negative := summonOptions(&config.Config{AgentName: "Juno", NameSummonEnabled: true})
	negative.NameSummonFollowUp = -time.Second
	noGate := summonOptions(&config.Config{AgentName: "Juno"})
	noGate.NameSummonFollowUp = time.Second

	for name, opts := range map[string]Options{"negative": negative, "no gate": noGate} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := New(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "NameSummonFollowUp") {
				t.Fatalf("err = %v, want a refusal naming NameSummonFollowUp", err)
			}
		})
	}
}

// The whole path a host gets: speech arrives, the gate decides, and only an
// addressed utterance, or a follow-up to a reply, reaches the brain.
func TestNameSummonGatesVoiceTurnsEndToEnd(t *testing.T) {
	heard := &scriptedSTT{}
	answering := &countingBrain{}
	opts := summonOptions(&config.Config{AgentName: "Juno", NameSummonEnabled: true})
	opts.STT, opts.Brain = heard, answering
	opts.NameSummonFollowUp = time.Minute
	var infos []string
	events.Subscribe(opts.Events, func(e events.Info) { infos = append(infos, e.Message) })

	agent, cleanup, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	say := func(text string) {
		t.Helper()
		heard.next = text
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := agent.RunTurn(ctx); err != nil {
			t.Fatalf("RunTurn(%q): %v", text, err)
		}
	}

	say(unaddressed)
	if got := answering.count(); got != 0 {
		t.Fatalf("an utterance for someone else reached the brain (%d calls)", got)
	}
	if len(infos) == 0 || !strings.Contains(infos[len(infos)-1], "Not addressed to Juno") {
		t.Fatalf("the dropped utterance was not reported: %v", infos)
	}

	say("Hey Juno, what time is it?")
	if got := answering.count(); got != 1 {
		t.Fatalf("an addressed utterance made %d brain calls, want 1", got)
	}

	say("and tomorrow?")
	if got := answering.count(); got != 2 {
		t.Fatalf("a follow-up right after the reply made %d brain calls in total, want 2", got)
	}
}

// scriptedSTT hands the pipeline one finished transcript per turn.
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

type countingBrain struct {
	stubBrain
	mu    sync.Mutex
	calls int
}

func (b *countingBrain) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func (b *countingBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	chunks := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	chunks <- "Half past nine."
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (b *countingBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	return "Half past nine.", nil
}
