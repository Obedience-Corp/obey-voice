package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
)

// PersonaContext holds private model memory separately from the shared transcript.
// Through and PrefixHash identify the canonical prefix already incorporated.
type PersonaContext struct {
	Working          []Turn             `json:"working"`
	Archive          []continuity.Entry `json:"archive,omitempty"`
	ContextHighWater int                `json:"context_high_water,omitempty"`
	Through          int                `json:"through"`
	PrefixHash       string             `json:"prefix_hash"`
}
type PersonaContexts map[string]PersonaContext

func (contexts PersonaContexts) Clone() PersonaContexts {
	out := make(PersonaContexts, len(contexts))
	for id, state := range contexts {
		state.Working = deepCopyTurns(state.Working)
		state.Archive = slices.Clone(state.Archive)
		out[id] = state
	}
	return out
}

// CloneTurns returns a copy that shares no mutable state with in.
func CloneTurns(in []Turn) []Turn { return deepCopyTurns(in) }

// Stable turn identity survives later native captions and response disposition
// updates. Legacy turns without IDs fall back to their original content.
func contextTurnKey(t Turn) string {
	if t.ResponseID != "" {
		// One response can contain several assistant/tool turns. Its response ID
		// alone is not a turn identity. Timestamps and tool pairing survive JSON
		// round trips; legacy untimestamped turns retain a content discriminator.
		key := struct {
			Role, ResponseID, At, ToolCallID, ToolName, LegacyContent string
			Calls                                                     []TurnToolCall
		}{Role: t.Role, ResponseID: t.ResponseID, At: t.At.UTC().Format(time.RFC3339Nano), ToolCallID: t.ToolCallID, ToolName: t.ToolName, Calls: t.ToolCalls}
		if t.At.IsZero() {
			key.LegacyContent = t.Content
		}
		data, _ := json.Marshal(key)
		return string(data)
	}
	if t.Utterance != nil && t.Utterance.ID != "" {
		return t.Role + ":utterance:" + t.Utterance.ID + ":" + strconv.FormatUint(t.Utterance.CaptureEpoch, 10)
	}
	if t.Role == "tool" && t.ToolCallID != "" {
		return "tool:" + t.ToolCallID
	}
	data, _ := json.Marshal(t)
	return string(data)
}
func contextPrefixHash(turns []Turn) string {
	keys := make([]string, len(turns))
	for i, t := range turns {
		keys[i] = contextTurnKey(t)
	}
	data, _ := json.Marshal(keys)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func CapturePersonaContext(snapshot ContinuitySnapshot) PersonaContext {
	working := snapshot.Working
	if working == nil {
		working = snapshot.Canonical
	}
	return PersonaContext{Working: deepCopyTurns(working), Archive: slices.Clone(snapshot.Archive), ContextHighWater: snapshot.ContextHighWater, Through: len(snapshot.Canonical), PrefixHash: contextPrefixHash(snapshot.Canonical)}
}

// Reconcile restores this persona's memory and adds only unseen shared turns.
// A divergent/replaced transcript cannot inherit a private context by position.
func (state PersonaContext) Reconcile(canonical []Turn) ContinuitySnapshot {
	valid := state.Through >= 0 && state.Through <= len(canonical) && state.PrefixHash == contextPrefixHash(canonical[:state.Through])
	if !valid {
		return ContinuitySnapshot{Canonical: deepCopyTurns(canonical), Working: deepCopyTurns(canonical)}
	}
	working := deepCopyTurns(state.Working)
	current := make(map[string]Turn, len(canonical))
	for _, turn := range canonical {
		current[contextTurnKey(turn)] = turn
	}
	for i, turn := range working {
		if latest, ok := current[contextTurnKey(turn)]; ok {
			// Preserve persona-specific bounded tool projections and summaries.
			// Only later authoritative status/caption updates need reconciliation.
			working[i].Disposition = latest.Disposition
			if turn.Role == "user" && latest.Utterance != nil {
				working[i].Utterance = latest.Utterance.Clone()
				working[i].Content = latest.Content
				working[i].AudioRetained = latest.AudioRetained
			}
		}
	}
	working = append(working, deepCopyTurns(canonical[state.Through:])...)
	return ContinuitySnapshot{Canonical: deepCopyTurns(canonical), Working: working, Archive: slices.Clone(state.Archive), ContextHighWater: state.ContextHighWater}
}
