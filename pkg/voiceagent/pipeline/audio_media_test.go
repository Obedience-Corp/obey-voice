package pipeline

import (
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestClearReleasesCanonicalAudioAfterStandardSwitchAndPreservesBorrowedHolds(t *testing.T) {
	for _, extraHold := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical only", true: "borrowed preview hold"}[extraHold], func(t *testing.T) {
			media := chatmedia.NewAudioSession()
			defer media.Close()
			ref, err := media.ImportSamples(t.Context(), []float32{.1, .2})
			if err != nil {
				t.Fatal(err)
			}
			input := &utterance.Finalized{ID: "recording", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}
			responder := &captionFixtureBrain{nativeInputBrain: nativeInputBrain{fakeBrain: fakeBrain{chunks: []string{"answer"}}}}
			p := &Pipeline{Brain: responder, Native: &NativePolicy{Controls: "conversation", Transcript: "off", Media: media}, Input: &fixtureNativeInput{input: input}}
			p.SetOutputMuted(true)
			if _, err := p.RunTurn(t.Context()); err != nil {
				t.Fatal(err)
			}
			if extraHold {
				if err := media.Retain(ref); err != nil {
					t.Fatal(err)
				}
			}
			replacement, err := p.PrepareInputReplacement(t.Context(), InputBinding{STT: idleBoundaryProvider()})
			if err != nil {
				t.Fatal(err)
			}
			replacement.Publish()
			if p.Native != nil {
				t.Fatal("test did not switch to Standard input")
			}
			if err := p.ClearConversation(); err != nil {
				t.Fatal(err)
			}
			if len(responder.History()) != 0 {
				t.Fatal("clear left canonical audio history")
			}
			if extraHold {
				if _, err := media.ResolveAudio(t.Context(), ref); err != nil {
					t.Fatal("clear released another consumer's reference", err)
				}
				media.Release(ref)
			}
			if _, err := media.ResolveAudio(t.Context(), ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
				t.Fatal("cleared canonical bytes remain", err)
			}
			// The borrowed owner stays usable for the conversation's next recording.
			next, err := media.ImportSamples(t.Context(), []float32{.3})
			if err != nil {
				t.Fatal(err)
			}
			media.Release(next)
		})
	}
}

func TestInputReplacementCannotSubstituteCanonicalMediaOwner(t *testing.T) {
	oldMedia, newMedia := chatmedia.NewAudioSession(), chatmedia.NewAudioSession()
	defer oldMedia.Close()
	defer newMedia.Close()
	ref, err := oldMedia.ImportSamples(t.Context(), []float32{.1})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Native: &NativePolicy{Media: oldMedia}, Brain: &fakeBrain{}}
	if err := p.rememberAudioMedia(oldMedia); err != nil {
		t.Fatal(err)
	}
	lease, err := p.PrepareInputReplacement(t.Context(), InputBinding{TextOnly: true, Native: p.Native})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Discard()
	if err := lease.Stage(InputBinding{TextOnly: true, Native: &NativePolicy{Media: newMedia}}); !errors.Is(err, ErrAudioMediaOwner) {
		t.Fatal(err)
	}
	if _, err := oldMedia.ResolveAudio(t.Context(), ref); err != nil {
		t.Fatal("failed replacement lost old media", err)
	}
}

func TestRejectedFinalizedMetadataReleasesItsOwnedAudioReference(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{.1})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Native: &NativePolicy{Media: media}, Brain: &fakeBrain{}}
	metrics := newTurnMetrics()
	invalid := &utterance.Finalized{ID: "", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}
	if _, err := p.resolveNativeInput(t.Context(), invalid, metrics); err == nil {
		t.Fatal("invalid input metadata admitted")
	}
	p.releaseUncommittedInput(metrics)
	if _, err := media.ResolveAudio(t.Context(), ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal("invalid finalized metadata leaked owned audio", err)
	}
}
