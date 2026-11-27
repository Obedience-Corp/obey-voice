package voiceagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/kws"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/summon"
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
type scriptedSTT struct {
	next    string
	heard   string
	heardBy time.Duration
}

func (s *scriptedSTT) Available() bool { return true }
func (s *scriptedSTT) Start(context.Context) (stt.Session, error) {
	ch := make(chan stt.Event, 1)
	ch <- stt.FinalTranscript{Text: s.next, AcousticName: s.heard, AcousticNameBy: s.heardBy}
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

// A recogniser that misspells the name is rescued by the keyword spotter's
// hit, but only at the start of the utterance and only when the config asks.
func TestNameSummonAcousticEvidenceEndToEnd(t *testing.T) {
	const misheard = "Juneau, what's the weather?"
	for _, tc := range []struct {
		why      string
		acoustic bool
		window   time.Duration
		text     string
		heard    string
		by       time.Duration
		answered bool
	}{
		{"hit at the start", true, 0, misheard, "Juno", time.Second, true},
		{"no hit", true, 0, misheard, "", 0, false},
		{"hit late in the utterance", true, 0, "Yesterday we talked about Juneau.", "Juno", 4 * time.Second, false},
		{"spelled mention", true, 0, "Tell me about Juno.", "Juno", time.Second, false},
		{"acoustic_name_enabled off", false, 0, misheard, "Juno", time.Second, false},
		{"window switched off by the host", true, -1, misheard, "Juno", time.Second, false},
		{"host window too short for this hit", true, 500 * time.Millisecond, misheard, "Juno", time.Second, false},
	} {
		t.Run(tc.why, func(t *testing.T) {
			heard := &scriptedSTT{next: tc.text, heard: tc.heard, heardBy: tc.by}
			answering := &countingBrain{}
			opts := summonOptions(&config.Config{AgentName: "Juno", NameSummonEnabled: true, AcousticNameEnabled: tc.acoustic})
			opts.STT, opts.Brain = heard, answering
			opts.NameSummonAcousticWindow = tc.window
			agent, cleanup, err := New(context.Background(), opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := agent.RunTurn(ctx); err != nil {
				t.Fatalf("RunTurn: %v", err)
			}
			if got := answering.count() > 0; got != tc.answered {
				t.Fatalf("answered=%v, want %v", got, tc.answered)
			}
		})
	}
}

func TestAcousticNamesFollowTheGate(t *testing.T) {
	cfg := &config.Config{AgentName: "Juno", NameSummonAliases: []string{"R2D2", "the computer"}, NameSummonEnabled: true, AcousticNameEnabled: true}
	gate, err := buildNameSummonGate(cfg, summonOptions(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var logged string
	got := withAcousticNames(cfg, gate, func(format string, args ...any) { logged = fmt.Sprintf(format, args...) })
	if !slices.Equal(got.AcousticNames, []string{"the computer", "juno"}) {
		t.Fatalf("spotter names = %q", got.AcousticNames)
	}
	if cfg.AcousticNames != nil {
		t.Fatal("the caller's config was changed")
	}
	if !strings.Contains(logged, `cannot spell ["r2d2"]`) {
		t.Fatalf("the skipped alias was not reported: %q", logged)
	}
	// No gate, or the detector off: the recogniser's config is the caller's own.
	if withAcousticNames(cfg, nil, nil) != cfg {
		t.Fatal("a config without a gate was copied")
	}
	off := &config.Config{AgentName: "Juno", NameSummonEnabled: true}
	if withAcousticNames(off, gate, nil) != off {
		t.Fatal("a config without acoustic_name_enabled was copied")
	}
}

func TestNewRefusesAcousticNamesItCannotSpell(t *testing.T) {
	cfg := &config.Config{AgentName: "R2D2", NameSummonEnabled: true, AcousticNameEnabled: true}
	_, _, err := New(context.Background(), summonOptions(cfg))
	if !errors.Is(err, kws.ErrUnspellable) {
		t.Fatalf("New = %v, want ErrUnspellable", err)
	}
}

func TestAcousticHookIsInstalledOnlyWhenInForce(t *testing.T) {
	for _, tc := range []struct {
		why      string
		acoustic bool
		window   time.Duration
		hook     bool
	}{
		{"acoustic_name_enabled", true, 0, true},
		{"acoustic_name_enabled off", false, 0, false},
		{"switched off by the host", true, -1, false},
	} {
		opts := summonOptions(&config.Config{AgentName: "Juno", NameSummonEnabled: true, AcousticNameEnabled: tc.acoustic})
		opts.NameSummonAcousticWindow = tc.window
		agent, cleanup, err := New(context.Background(), opts)
		if err != nil {
			t.Fatalf("%s: New: %v", tc.why, err)
		}
		if got := agent.AcousticAddressGate != nil; got != tc.hook || agent.AddressGate == nil {
			t.Errorf("%s: acoustic hook installed=%v, want %v", tc.why, got, tc.hook)
		}
		cleanup()
	}
}

func TestNewRejectsAnAcousticWindowThatCannotApply(t *testing.T) {
	for why, cfg := range map[string]*config.Config{
		"acoustic_name_enabled off": {AgentName: "Juno", NameSummonEnabled: true},
		"name summon off":           {AgentName: "Juno", AcousticNameEnabled: true},
	} {
		opts := summonOptions(cfg)
		opts.NameSummonAcousticWindow = time.Second
		if _, _, err := New(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "NameSummonAcousticWindow") {
			t.Errorf("%s: New = %v", why, err)
		}
	}
}

// The hit time counts from the start of the pre-roll, so the default window
// has to move with it when New builds the recogniser.
func TestAcousticWindowFollowsPreRoll(t *testing.T) {
	cfg := &config.Config{AcousticNameEnabled: true, VADPreRollMS: 500}
	if got, want := acousticWindow(cfg, Options{}), summon.DefaultAcousticWindow+200*time.Millisecond; got != want {
		t.Errorf("New's own recogniser, 500 ms pre-roll: %s, want %s", got, want)
	}
	cfg.VADPreRollMS = 300
	if got := acousticWindow(cfg, Options{}); got != summon.DefaultAcousticWindow {
		t.Errorf("default pre-roll: %s", got)
	}
	cfg.VADPreRollMS = 500
	if got := acousticWindow(cfg, Options{STT: &stubSTT{}}); got != summon.DefaultAcousticWindow {
		t.Errorf("an injected recogniser segments its own audio: %s", got)
	}
	if got := acousticWindow(cfg, Options{NameSummonAcousticWindow: time.Second}); got != time.Second {
		t.Errorf("a host's own window was changed: %s", got)
	}
	if got := acousticWindow(cfg, Options{NameSummonAcousticWindow: -1}); got != 0 {
		t.Errorf("switched off: %s", got)
	}
	if got := acousticWindow(&config.Config{}, Options{}); got != 0 {
		t.Errorf("acoustic_name_enabled off: %s", got)
	}
}
