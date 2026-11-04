package session

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// SelectionState records the effective admitted mode/revision without endpoint
// credentials or mutable profile contents. Restore must revalidate dependencies;
// this snapshot never authorizes an unavailable persona to start capture.
type SelectionState struct {
	BindingHash     string `json:"binding_hash,omitempty"`
	Revision        uint64 `json:"revision"`
	AudioMode       string `json:"audio_mode"`
	AudioControls   string `json:"audio_controls,omitempty"`
	AudioTranscript string `json:"audio_transcript,omitempty"`
	AudioRetained   bool   `json:"audio_retained,omitempty"`
	BrainProvider   string `json:"brain_provider,omitempty"`
	BrainModel      string `json:"brain_model,omitempty"`
	VoiceProvider   string `json:"voice_provider,omitempty"`
	Voice           string `json:"voice,omitempty"`
}

func cloneSelection(state *SelectionState) *SelectionState {
	if state == nil {
		return nil
	}
	copy := *state
	return &copy
}
func validateSelectionState(state *SelectionState) error {
	if state == nil {
		return nil
	}
	if state.BindingHash != "" {
		decoded, err := hex.DecodeString(state.BindingHash)
		if err != nil || len(decoded) != 32 {
			return errors.New("session has invalid selected binding hash")
		}
	}
	if state.AudioMode != "standard" && state.AudioMode != "native" {
		return errors.New("session has invalid selected audio mode")
	}
	if state.AudioMode == "native" {
		if state.BindingHash == "" || state.BrainProvider != "ollama" || state.BrainModel == "" || state.VoiceProvider != config.OpenAISpeechProviderName {
			return errors.New("session native selection has incomplete effective provider identity")
		}
		if state.AudioControls != "standard" && state.AudioControls != "conversation" {
			return errors.New("session has invalid native controls")
		}
		if state.AudioTranscript != "auto" && state.AudioTranscript != "required" && state.AudioTranscript != "off" {
			return errors.New("session has invalid native transcript policy")
		}
		if state.AudioControls == "standard" && state.AudioTranscript == "off" {
			return errors.New("session native Standard controls require transcription")
		}
	}
	return nil
}

// SaveSelectionState atomically persists the effective selection, preserving the
// same before/after-rename semantics and latest work snapshot as SaveSelection.
func (st *Store) SaveSelectionState(s *Session, turns []brain.Turn, selected events.PersonaSelection, contexts ...brain.PersonaContexts) error {
	mode := selected.AudioMode
	if mode == "" {
		mode = "standard"
	}
	state := &SelectionState{BindingHash: selected.BindingHash, Revision: selected.Revision, AudioMode: mode, AudioControls: selected.AudioControls, AudioTranscript: selected.AudioTranscript, AudioRetained: selected.AudioRetained, BrainProvider: selected.BrainProvider, BrainModel: selected.BrainModel, VoiceProvider: selected.VoiceProvider, Voice: selected.Voice}
	if err := validateSelectionState(state); err != nil {
		return err
	}
	sessionWriteMu.Lock()
	defer sessionWriteMu.Unlock()
	var owned brain.PersonaContexts
	if len(contexts) > 0 {
		owned = contexts[0].Clone()
	}
	return st.saveSelection(s, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash, state, owned, selected.Directories)
}

// SaveSelection preserves the legacy Standard-only API. New selector hosts use
// SaveSelectionState so mode/revision cannot be inferred from an edited profile.
func (st *Store) SaveSelection(s *Session, turns []brain.Turn, personaID, displayName, promptHash string, directories ...*config.ConversationDirectories) error {
	sessionWriteMu.Lock()
	defer sessionWriteMu.Unlock()
	return st.saveSelection(s, turns, personaID, displayName, promptHash, nil, nil, directories...)
}
func (st *Store) saveSelection(s *Session, turns []brain.Turn, personaID, displayName, promptHash string, state *SelectionState, contexts brain.PersonaContexts, directories ...*config.ConversationDirectories) error {
	current, err := loadFrom(st.dir, s.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load session before selection save: %w", err)
	}
	candidate := *s
	if err == nil {
		candidate = *current
	}
	if contexts != nil {
		candidate.PersonaContexts = contexts.Clone()
	}
	candidate.Turns = normalizeTurns(turns)
	candidate.SelectedPersonaID, candidate.SelectedPersonaDisplayName, candidate.SelectedPersonaPromptHash = personaID, displayName, promptHash
	candidate.Selection = cloneSelection(state)
	if len(directories) > 0 && directories[0] != nil {
		candidate.Directories = directories[0].Clone()
		if choice, ok := directories[0].Personas[personaID]; ok {
			candidate.WorkDir, candidate.WorkDirSource = choice.Dir, choice.Source
		}
	}
	if err := saveSessionWithFailures(st.dir, &candidate, candidate.Turns, candidate.Work, st.failBeforeRename, st.failAfterRename); err != nil {
		if WriteCommitted(err) {
			*s = candidate
		}
		return err
	}
	*s = candidate
	return nil
}
