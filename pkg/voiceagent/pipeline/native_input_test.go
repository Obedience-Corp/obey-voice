package pipeline

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type fixtureNativeInput struct {
	input  *utterance.Finalized
	starts int
}

func (p *fixtureNativeInput) Available() bool { return true }
func (p *fixtureNativeInput) Start(context.Context) (utterance.Session, error) {
	p.starts++
	events := make(chan utterance.Event, 1)
	events <- utterance.Event{Kind: utterance.FinalizedEvent, Input: p.input.Clone()}
	close(events)
	return &fixtureNativeSession{events: events}, nil
}

type fixtureNativeSession struct{ events chan utterance.Event }

func (s *fixtureNativeSession) Events() <-chan utterance.Event { return s.events }
func (*fixtureNativeSession) Close() error                     { return nil }

type forbiddenStandardSTT struct{ starts int }

func (s *forbiddenStandardSTT) Available() bool { return true }
func (s *forbiddenStandardSTT) Start(context.Context) (stt.Session, error) {
	s.starts++
	return nil, errors.New("Standard recognizer must not start")
}

type nativeInputBrain struct {
	fakeBrain
	opts  brain.StreamOptions
	input string
	turns []brain.Turn
}

func (b *nativeInputBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.input = input
	b.opts = opts
	b.turns = append(b.turns, brain.Turn{Role: "user", Utterance: opts.Utterance.Clone(), AudioRetained: opts.AudioRetained})
	return b.fakeBrain.ThinkStream(ctx, input, opts)
}
func (b *nativeInputBrain) History() []brain.Turn { return b.turns }
func TestNativeConversationOffAdmitsAudioWithoutStandardSTTOrSpokenControls(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "fixture", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}
	provider := &fixtureNativeInput{input: input}
	standard := &forbiddenStandardSTT{}
	responder := &nativeInputBrain{fakeBrain: fakeBrain{chunks: []string{"Hello."}}}
	bus := events.NewBus()
	var user events.UserInput
	events.Subscribe(bus, func(event events.UserInput) { user = event })
	pipeline := &Pipeline{Input: provider, STT: standard, Native: &NativePolicy{Controls: "conversation", Transcript: "off", Media: media}, Brain: responder, Events: bus, VoiceToolsEnabled: true}
	pipeline.SetOutputMuted(true)
	pipeline.AddressGate = func(string) string { t.Error("spoken name gate ran"); return "blocked" }
	pipeline.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		t.Error("spoken controls ran")
		return ResponseAdmission{}, errors.New("unexpected")
	}
	pipeline.AdmitAudio = func(context.Context, *utterance.Finalized) (ResponseAdmission, error) {
		return ResponseAdmission{}, nil
	}
	text, err := pipeline.RunTurn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if text != "" || provider.starts != 1 || standard.starts != 0 {
		t.Fatal(text, provider.starts, standard.starts)
	}
	if responder.opts.Utterance == nil || responder.opts.Utterance.Transcript.State != utterance.Disabled || responder.opts.ToolsEnabled || responder.input != "" {
		t.Fatal(responder.opts, responder.input)
	}
	if user.Utterance == nil || user.Text != "" {
		t.Fatal("audio event lost metadata or fabricated text", user)
	}
	if _, err := media.ResolveAudio(t.Context(), ref); err != nil {
		t.Fatal("canonical recording released early", err)
	}
}

func (b *nativeInputBrain) ImportHistory(turns []brain.Turn) error { b.turns = turns; return nil }
func TestNativeStandardControlsTranscribeOnceAndFinishLocalCommandWithoutChat(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Error(r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Samantha, switch voice"}`)
	}))
	defer server.Close()
	client, err := nativeaudio.NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	provider := &fixtureNativeInput{input: &utterance.Finalized{ID: "local-command", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}}
	responder := &nativeInputBrain{}
	pipeline := &Pipeline{Input: provider, Native: &NativePolicy{Controls: "standard", Transcript: "auto", Media: media, Client: client, Scheduler: nativeaudio.NewScheduler()}, Brain: responder, Events: events.NewBus()}
	pipeline.SetOutputMuted(true)
	admitted := 0
	pipeline.AdmitInput = func(_ context.Context, text string, _ bool) (ResponseAdmission, error) {
		admitted++
		if text != "Samantha, switch voice" {
			t.Error(text)
		}
		return ResponseAdmission{LocalReply: "Voice changed."}, nil
	}
	if _, err := pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || admitted != 1 || responder.opts.Utterance != nil {
		t.Fatal("ASR/admission/chat calls", calls, admitted, responder.opts)
	}
	if len(responder.turns) != 2 || responder.turns[0].Utterance == nil || responder.turns[0].Content != "Samantha, switch voice" {
		t.Fatal("local history lost recording evidence", responder.turns)
	}
}
func TestNativeRequiredTranscriptionFailsBeforeAdmissionChatOrSpeech(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	client, err := nativeaudio.NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	provider := &fixtureNativeInput{input: &utterance.Finalized{ID: "failed-asr", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}}
	responder := &nativeInputBrain{}
	speech := &groupingFakeTTS{}
	pipeline := &Pipeline{Input: provider, Native: &NativePolicy{Controls: "standard", Transcript: "required", Media: media, Client: client, Scheduler: nativeaudio.NewScheduler()}, Brain: responder, TTS: speech, Events: events.NewBus()}
	pipeline.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		t.Error("failed ASR reached admission")
		return ResponseAdmission{}, nil
	}
	if _, err := pipeline.RunTurn(t.Context()); err == nil {
		t.Fatal("failed ASR admitted")
	}
	if responder.opts.Utterance != nil || len(speech.Texts()) != 0 {
		t.Fatal("failed ASR reached chat/speech")
	}
	if _, err := media.ResolveAudio(t.Context(), ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal("failed input leaked owned audio", err)
	}
}
