package session

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func savedAudioRestoreFixture(t *testing.T, state utterance.State, retained bool) (*Store, *Session, utterance.AudioRef, []byte, chatmedia.Store) {
	t.Helper()
	store := NewStore(t.TempDir())
	saved := New("ollama", "audio")
	source := chatmedia.NewAudioSession()
	defer source.Close()
	ref, err := source.ImportSamples(t.Context(), []float32{.1, .2, -.2})
	if err != nil {
		t.Fatal(err)
	}
	data, err := source.ResolveAudio(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := store.AudioMedia(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained {
		if err := disk.PersistAudio(t.Context(), ref, data); err != nil {
			t.Fatal(err)
		}
	}
	transcript := utterance.Transcript{State: state, Provider: "ollama", Model: "selected"}
	if state == utterance.Ready {
		transcript.Text = "actual recording words"
	}
	input := &utterance.Finalized{ID: "recording", CaptureEpoch: 17, Audio: &ref, Transcript: transcript}
	turns := []brain.Turn{{Role: "user", Content: transcript.Text, TypedInput: "consider this recording", Utterance: input, AudioRetained: retained}}
	if err := store.Save(saved, turns); err != nil {
		t.Fatal(err)
	}
	return store, saved, ref, data, disk
}

func TestRestoreAudioMakesInterruptedCaptionUnavailableWithoutInferenceOrDiskMutation(t *testing.T) {
	store, saved, ref, data, _ := savedAudioRestoreFixture(t, utterance.Pending, true)
	owner := chatmedia.NewAudioSession()
	defer owner.Close()
	restored, err := store.RestoreAudio(t.Context(), saved.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	input := restored.Session.Turns[0].Utterance
	if input.Transcript.State != utterance.Unavailable || input.Transcript.Reason != "interrupted before transcription finished" || input.Transcript.Model != "selected" || restored.ContextError != nil || len(restored.Replay) != 1 || !restored.Replay[0].Available {
		t.Fatal("incorrect interrupted/replay state", restored.Replay, input)
	}
	got, err := restored.ResolveReplay(t.Context(), saved.ID, input.ID, input.CaptureEpoch)
	if err != nil || string(got) != string(data) {
		t.Fatal("restored replay changed samples", err)
	}
	for _, identity := range []struct {
		session, id string
		epoch       uint64
	}{{"other-session", input.ID, input.CaptureEpoch}, {saved.ID, "other-recording", input.CaptureEpoch}, {saved.ID, input.ID, input.CaptureEpoch + 1}} {
		if _, err := restored.ResolveReplay(t.Context(), identity.session, identity.id, identity.epoch); !errors.Is(err, utterance.ErrAudioUnavailable) {
			t.Fatal("wrong replay identity authorized", err)
		}
	}
	raw, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Turns[0].Utterance.Transcript.State != utterance.Pending {
		t.Fatal("restore rewrote canonical disk evidence")
	}
	restored.Close()
	if _, err := owner.ResolveAudio(t.Context(), ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal("discarded restore leaked imported hold", err)
	}
}

func TestRestoreAudioMissingAndMemoryOnlyContextRemainsExplicit(t *testing.T) {
	for _, state := range []utterance.State{utterance.Ready, utterance.Pending, utterance.Disabled} {
		for _, retained := range []bool{false, true} {
			t.Run(string(state)+map[bool]string{false: " memory only", true: " missing retained"}[retained], func(t *testing.T) {
				store, saved, _, _, disk := savedAudioRestoreFixture(t, state, retained)
				if retained {
					if err := disk.RemoveOwned(); err != nil {
						t.Fatal(err)
					}
				}
				owner := chatmedia.NewAudioSession()
				defer owner.Close()
				restored, err := store.RestoreAudio(t.Context(), saved.ID, owner)
				if err != nil {
					t.Fatal(err)
				}
				defer restored.Close()
				if restored.Replay[0].Available || restored.PersistenceUnavailable != retained {
					t.Fatal(restored.Replay, restored.PersistenceUnavailable)
				}
				if state == utterance.Ready {
					if restored.ContextError != nil || restored.Session.Turns[0].Utterance.Transcript.Text != "actual recording words" {
						t.Fatal("ready transcript projection lost")
					}
				} else if !errors.Is(restored.ContextError, brain.ErrAudioProjection) {
					t.Fatal("unavailable audio-only context silently omitted", restored.ContextError)
				}
				if _, err := os.Stat(disk.Dir); !os.IsNotExist(err) {
					t.Fatal("restore regenerated missing media", err)
				}
			})
		}
	}
}

func TestRestoreAudioAdoptionTransfersCanonicalHoldsAndFailedPartialRestoreReleasesThem(t *testing.T) {
	store, saved, ref, _, _ := savedAudioRestoreFixture(t, utterance.Ready, true)
	owner := chatmedia.NewAudioSession()
	defer owner.Close()
	restored, err := store.RestoreAudio(t.Context(), saved.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Adopt(); err != nil {
		t.Fatal(err)
	}
	restored.Close()
	if _, err := owner.ResolveAudio(t.Context(), ref); err != nil {
		t.Fatal("adopted recording released before canonical owner", err)
	}
	owner.Release(ref)
	duplicate := append(saved.Turns, saved.Turns...)
	if err := store.Save(saved, duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreAudio(t.Context(), saved.ID, owner); err == nil {
		t.Fatal("duplicate utterance identity restored ambiguously")
	}
	if _, err := owner.ResolveAudio(t.Context(), ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal("failed partial restoration leaked audio", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.RestoreAudio(cancelled, saved.ID, owner); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
