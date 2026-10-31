package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type ReplayAvailability struct {
	UtteranceID  string `json:"utterance_id"`
	CaptureEpoch uint64 `json:"capture_epoch"`
	Available    bool   `json:"available"`
	Reason       string `json:"reason,omitempty"`
}
type replayIdentity struct {
	id    string
	epoch uint64
}

// AudioRestoration is an inert, readable session candidate. It does no inference,
// capture, playback, asset regeneration or persistence. The caller must inspect
// context/persistence availability before resuming. Close discards imported
// references on failure; Adopt transfers those holds to canonical history only
// after the host's lifecycle transaction commits.
type AudioRestoration struct {
	Session                *Session
	Replay                 []ReplayAvailability
	ContextError           error
	PersistenceUnavailable bool
	mu                     sync.Mutex
	media                  *chatmedia.AudioSession
	sessionID              string
	refs                   map[replayIdentity]utterance.AudioRef
	holds                  []utterance.AudioRef
	adopted, closed        bool
}

func (st *Store) RestoreAudio(ctx context.Context, id string, media *chatmedia.AudioSession) (*AudioRestoration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if media == nil {
		return nil, errors.New("session audio restore requires an owned media session")
	}
	saved, err := st.Load(id)
	if err != nil {
		return nil, err
	}
	saved.Turns = normalizeTurns(saved.Turns)
	restored := &AudioRestoration{Session: saved, media: media, sessionID: id, refs: make(map[replayIdentity]utterance.AudioRef)}
	success := false
	defer func() {
		if !success {
			restored.Close()
		}
	}()
	disk, diskErr := st.AudioMedia(id)
	seen := make(map[replayIdentity]bool)
	for i := range saved.Turns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		turn := &saved.Turns[i]
		input := turn.Utterance
		if input == nil {
			continue
		}
		key := replayIdentity{input.ID, input.CaptureEpoch}
		if seen[key] {
			return nil, errors.New("session has duplicate recording identity")
		}
		seen[key] = true
		if input.Transcript.State == utterance.Pending {
			input.Transcript = utterance.Transcript{State: utterance.Unavailable, Provider: input.Transcript.Provider, Model: input.Transcript.Model, Reason: "interrupted before transcription finished"}
		}
		availability := ReplayAvailability{UtteranceID: input.ID, CaptureEpoch: input.CaptureEpoch, Reason: "recording was not retained"}
		if turn.AudioRetained {
			err := diskErr
			if err == nil {
				err = media.RestoreAudio(ctx, disk, *input.Audio)
			}
			if err == nil {
				availability.Available = true
				availability.Reason = ""
				restored.refs[key] = *input.Audio
				restored.holds = append(restored.holds, *input.Audio)
			} else {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if errors.Is(err, utterance.ErrBudgetExceeded) {
					availability.Reason = "session audio budget exceeded"
				} else {
					availability.Reason = "retained recording is missing or invalid"
					restored.PersistenceUnavailable = true
				}
			}
		}
		if !availability.Available && input.Transcript.State != utterance.Ready {
			restored.ContextError = errors.Join(restored.ContextError, fmt.Errorf("utterance %s: %w", input.ID, brain.ErrAudioProjection))
		}
		restored.Replay = append(restored.Replay, availability)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	success = true
	return restored, nil
}

func (r *AudioRestoration) Adopt() error {
	if r == nil {
		return errors.New("session audio restoration is missing")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("session audio restoration was discarded")
	}
	r.adopted = true
	r.holds = nil
	return nil
}
func (r *AudioRestoration) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed || r.adopted {
		r.mu.Unlock()
		return
	}
	r.closed = true
	holds := r.holds
	r.holds = nil
	r.mu.Unlock()
	for _, ref := range holds {
		r.media.Release(ref)
	}
}

// ResolveReplay returns verified WAV only for a successfully restored recording
// in this exact session. Hosts must apply their current lifecycle/playback fence
// before device use; bytes never belong in protocol events or transcript logs.
func (r *AudioRestoration) ResolveReplay(ctx context.Context, sessionID, id string, epoch uint64) ([]byte, error) {
	if r == nil {
		return nil, utterance.ErrAudioUnavailable
	}
	r.mu.Lock()
	if r.closed || sessionID != r.sessionID {
		r.mu.Unlock()
		return nil, utterance.ErrAudioUnavailable
	}
	ref, ok := r.refs[replayIdentity{id, epoch}]
	r.mu.Unlock()
	if !ok {
		return nil, utterance.ErrAudioUnavailable
	}
	return r.media.ResolveAudio(ctx, ref)
}
