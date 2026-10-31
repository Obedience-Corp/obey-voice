package session

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

func requiredFormatVersion(current int, turns []brain.Turn) int {
	version := max(current, StandardFormatVersion)
	for _, turn := range turns {
		if turn.Utterance != nil || turn.AudioRetained || turn.TypedInput != "" {
			return max(version, 3)
		}
	}
	return version
}

// AudioMedia scopes retained bytes to this session in the existing session
// store. Default/global media lookup is never used for audio replay.
func (st *Store) AudioMedia(id string) (chatmedia.Store, error) {
	if err := validateSessionID(id); err != nil {
		return chatmedia.Store{}, err
	}
	return chatmedia.Store{Dir: filepath.Join(st.dir, "media", id)}, nil
}
func validateAudioMetadata(turns []brain.Turn) error {
	for _, turn := range turns {
		if turn.Utterance == nil {
			if turn.AudioRetained || turn.TypedInput != "" {
				return fmt.Errorf("audio fields require utterance metadata")
			}
			continue
		}
		if turn.Role != "user" {
			return fmt.Errorf("utterance audio must belong to a user turn")
		}
		if err := turn.Utterance.Validate(); err != nil {
			return fmt.Errorf("session utterance: %w", err)
		}
	}
	return nil
}
func validateAudioTurns(dir, id string, turns []brain.Turn) error {
	if err := validateAudioMetadata(turns); err != nil {
		return err
	}
	store, err := NewStore(dir).AudioMedia(id)
	if err != nil {
		return err
	}
	for _, turn := range turns {
		if turn.AudioRetained {
			if _, err := store.ReadAudio(context.Background(), *turn.Utterance.Audio); err != nil {
				return fmt.Errorf("retained audio must publish before session: %w", err)
			}
		}
	}
	return nil
}
