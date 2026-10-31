package pipeline

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestRejectedNativeAddressCreatesNoRetainedFile(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"text":"words addressed elsewhere"}`)
	}))
	defer server.Close()
	client, err := nativeaudio.NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	store := chatmedia.Store{Dir: t.TempDir()}
	pipeline := &Pipeline{Input: &fixtureNativeInput{input: &utterance.Finalized{ID: "rejected", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}}, Native: &NativePolicy{Controls: "standard", Transcript: "required", Media: media, Store: &store, RetainAudio: true, Client: client, Scheduler: nativeaudio.NewScheduler(), Lifetime: t.Context()}, Brain: &nativeInputBrain{}, Events: events.NewBus(), AddressGate: func(string) string { return "not addressed to this persona" }}
	if _, err := pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.AudioPath(ref)); !os.IsNotExist(err) {
		t.Fatal("rejected input left retained audio", err)
	}
	if _, err := media.ResolveAudio(t.Context(), ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal("rejected input leaked memory", err)
	}
}

type rejectedNativeBrain struct{ nativeInputBrain }

func (*rejectedNativeBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("request rejected before canonical admission")
}
func TestFailedNativeDispatchCreatesNoRetainedFile(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	store := chatmedia.Store{Dir: t.TempDir()}
	pipeline := &Pipeline{Input: &fixtureNativeInput{input: &utterance.Finalized{ID: "dispatch-failure", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}}, Native: &NativePolicy{Controls: "conversation", Transcript: "off", Media: media, Store: &store, RetainAudio: true, Lifetime: t.Context()}, Brain: &rejectedNativeBrain{}, Events: events.NewBus()}
	_, _ = pipeline.RunTurn(t.Context())
	if _, err := os.Stat(store.AudioPath(ref)); !os.IsNotExist(err) {
		t.Fatal("failed dispatch left retained audio", err)
	}
}
func TestNativeRetainedFileExistsBeforeCanonicalAutosave(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewStore(t.TempDir())
	saved := session.New("ollama", "chosen-model")
	store, err := sessions.AudioMedia(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	responder := &nativeInputBrain{fakeBrain: fakeBrain{chunks: []string{"Hello."}}}
	pipeline := &Pipeline{Input: &fixtureNativeInput{input: &utterance.Finalized{ID: "accepted", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}}, Native: &NativePolicy{Controls: "conversation", Transcript: "off", Media: media, Store: &store, RetainAudio: true, Lifetime: t.Context()}, Brain: responder, Events: events.NewBus()}
	pipeline.SetOutputMuted(true)
	saves := 0
	pipeline.OnTurn = func() {
		saves++
		if _, err := store.ReadAudio(t.Context(), ref); err != nil {
			t.Error("autosave preceded asset publication", err)
		}
		if err := sessions.Save(saved, responder.History()); err != nil {
			t.Error(err)
		}
	}
	if _, err := pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if saves != 1 {
		t.Fatal(saves)
	}
	reopened, err := sessions.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Turns) != 1 || !reopened.Turns[0].AudioRetained || reopened.Turns[0].Utterance.Audio.ID != ref.ID {
		t.Fatal(reopened)
	}
}
