package personaruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type selectorNativeVoice struct{}

func (selectorNativeVoice) Available() bool                       { return true }
func (selectorNativeVoice) ListVoices(string, string) []tts.Voice { return nil }
func (selectorNativeVoice) Synthesize(context.Context, string) (*audio.PCMStream, error) {
	return nil, errors.New("no device in selection fixture")
}

type nativeSelectorFixture struct {
	*selectorTestFixture
	candidate     *voiceagent.NativeCandidate
	publisher     func(context.Context, func() error) error
	inputReleased int
}

func newNativeSelectorFixture(t *testing.T) *nativeSelectorFixture {
	t.Helper()
	standard := testProfile("standard", "Standard", "standard")
	native := testProfile("native", "Native", "native")
	native.Audio = &persona.Audio{Mode: persona.AudioNative, Model: "selected", Endpoint: "http://localhost:18144", Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff}
	f := &nativeSelectorFixture{selectorTestFixture: newSelectorTestFixture(t, standard, native)}
	if err := f.pipeline.BeginConversationIdentity(f.session.ID); err != nil {
		t.Fatal(err)
	}
	media := chatmedia.NewAudioSession()
	t.Cleanup(media.Close)
	voices := &voiceagent.LiveTTSManager{RetireDelay: -1}
	t.Cleanup(voices.Close)
	f.selector.opts.Voices = voices
	f.selector.opts.NewVoice = func(context.Context, *config.Config) (*voiceagent.TTSSet, error) {
		return &voiceagent.TTSSet{Primary: selectorNativeVoice{}}, nil
	}
	f.selector.opts.Native = &NativeSelectionHooks{
		TextOnly: true,
		Prepare: func(ctx context.Context, cfg *config.Config, mode persona.Audio, selection events.PersonaSelection, publish func(context.Context, func() error) error) (*voiceagent.NativeCandidate, error) {
			hash, err := brain.PersonaPromptHash(cfg)
			if err != nil {
				return nil, err
			}
			f.publisher = publish
			if err := publish(ctx, func() error { t.Error("uncommitted caption published"); return nil }); err == nil {
				t.Error("candidate publisher authorized before commit")
			}
			f.candidate = &voiceagent.NativeCandidate{Brain: newSelectorTestBrain(hash, nil), Voice: &voiceagent.TTSSet{Primary: selectorNativeVoice{}}, Policy: &pipeline.NativePolicy{Lifetime: t.Context(), PublishCaption: publish, Controls: mode.Controls, Transcript: mode.Transcript, Media: media}, Setup: nativeaudio.SetupResult{ProtocolReady: true}}
			return f.candidate, nil
		},
		// This authorizes only deterministic fixture transport. Production has no
		// such qualification override and keeps activation unavailable.
		Authorize: func(context.Context, *persona.SessionBinding, *voiceagent.NativeCandidate) error { return nil },
		PrepareInput: func(ctx context.Context, cfg *config.Config, candidate *voiceagent.NativeCandidate) (pipeline.InputBinding, func(), error) {
			next := pipeline.InputBinding{TextOnly: true}
			if candidate != nil {
				next.Native = candidate.Policy
			}
			return next, func() { f.inputReleased++ }, nil
		},
	}
	return f
}

func TestNativeSelectionPublishesOneBindingAndRetiresOldPublisher(t *testing.T) {
	f := newNativeSelectorFixture(t)
	selected, err := f.selector.Select(t.Context(), "native", "test")
	if err != nil {
		t.Fatal(err)
	}
	if selected.AudioMode != persona.AudioNative || f.pipeline.Native != f.candidate.Policy || f.pipeline.Brain != f.candidate.Brain || f.selector.nativeCandidate != f.candidate {
		t.Fatal("partial native publication")
	}
	publishes := 0
	oldPublisher := f.publisher
	if err := oldPublisher(t.Context(), func() error { publishes++; return nil }); err != nil || publishes != 1 {
		t.Fatal(err)
	}
	oldBrain := f.candidate.Brain.(*selectorTestBrain)
	if _, err := f.selector.Select(t.Context(), "standard", "test"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldBrain.closed:
	case <-time.After(time.Second):
		t.Fatal("retired native brain not closed by pipeline")
	}
	if f.pipeline.Native != nil || f.inputReleased != 1 {
		t.Fatal("old native resources not retired")
	}
	if err := oldPublisher(t.Context(), func() error { t.Fatal("late caption published"); return nil }); err == nil {
		t.Fatal("retired publisher authorized")
	}
	f.selector.Close()
}

func TestNativeSelectionFailedPersistencePreservesOldBinding(t *testing.T) {
	f := newNativeSelectorFixture(t)
	before := f.selector.Snapshot()
	f.selector.opts.Persist = func([]brain.Turn, events.PersonaSelection) error { return errors.New("fixture durable write failed") }
	if _, err := f.selector.Select(t.Context(), "native", "test"); err == nil {
		t.Fatal("failed persistence activated native")
	}
	if f.pipeline.Brain != f.old || f.pipeline.Native != nil || f.old.IsClosed() || f.selector.Snapshot().BindingHash != before.BindingHash || f.inputReleased != 1 {
		t.Fatal("failed candidate changed live binding")
	}
	if !f.candidate.Brain.(*selectorTestBrain).IsClosed() {
		t.Fatal("failed candidate brain leaked")
	}
	if err := f.publisher(t.Context(), func() error { t.Fatal("failed candidate published"); return nil }); err == nil {
		t.Fatal("failed caption authorized")
	}
	f.selector.Close()
}

func TestNativeSelectionRequiresIndependentAuthorization(t *testing.T) {
	f := newNativeSelectorFixture(t)
	f.selector.opts.Native.Authorize = nil
	if _, err := f.selector.Select(t.Context(), "native", "test"); !errors.Is(err, ErrNativeUnqualified) {
		t.Fatal(err)
	}
	if f.pipeline.Native != nil || f.old.IsClosed() || !f.candidate.Brain.(*selectorTestBrain).IsClosed() {
		t.Fatal("protocol readiness bypassed qualification")
	}
	f.selector.Close()
}

func TestNativeSelectionRejectsUntranscribedHistoryBeforeStandardProjection(t *testing.T) {
	f := newNativeSelectorFixture(t)
	if _, err := f.selector.Select(t.Context(), "native", "test"); err != nil {
		t.Fatal(err)
	}
	ref, err := f.candidate.Policy.Media.ImportSamples(t.Context(), []float32{.1})
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "audio-context", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}
	responder := f.candidate.Brain.(*selectorTestBrain)
	if err := responder.ImportHistory([]brain.Turn{{Role: "user", Utterance: input, TypedInput: "use this recording"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(t.Context(), "standard", "test"); !errors.Is(err, brain.ErrAudioProjection) {
		t.Fatal(err)
	}
	if f.pipeline.Native != f.candidate.Policy || responder.IsClosed() {
		t.Fatal("failed projection retired old binding")
	}
	input.Transcript = utterance.Transcript{State: utterance.Ready, Text: "actual recording transcript", Provider: "ollama", Model: "selected"}
	if err := responder.ImportHistory([]brain.Turn{{Role: "user", Content: input.Transcript.Text, Utterance: input, TypedInput: "use this recording"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(t.Context(), "standard", "test"); err != nil {
		t.Fatal(err)
	}
	f.selector.Close()
}

func TestNativeSelectionRequiresMatchingConversationEpochOwner(t *testing.T) {
	f := newNativeSelectorFixture(t)
	if err := f.pipeline.BeginConversationIdentity("another-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(t.Context(), "native", "test"); err == nil {
		t.Fatal("mismatched session scope activated native")
	}
	if f.candidate != nil {
		t.Fatal("mismatched scope ran candidate setup")
	}
	f.selector.Close()
}

func TestNativeSelectionPreservesPriorPersonaForUnsupportedImageContext(t *testing.T) {
	f := newNativeSelectorFixture(t)
	if err := f.old.ImportHistory([]brain.Turn{{Role: "user", Content: "inspect image", Attachments: []chatmedia.Ref{{ID: strings.Repeat("a", 64), Format: "png", Width: 1, Height: 1, Frames: 1}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(t.Context(), "native", "test"); !errors.Is(err, brain.ErrImageProjection) {
		t.Fatal(err)
	}
	if f.pipeline.Brain != f.old || f.old.IsClosed() || f.pipeline.Native != nil {
		t.Fatal("image projection failure changed selected persona")
	}
	if !f.candidate.Brain.(*selectorTestBrain).IsClosed() {
		t.Fatal("unsupported context candidate leaked")
	}
	f.selector.Close()
}
