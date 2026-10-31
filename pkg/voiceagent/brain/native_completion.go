package brain

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/ollama/ollama/api"
)

// Completion has an intentionally bounded, private conversation excerpt. Native
// must still represent the recordings in that excerpt, rather than replacing a
// spoken question with its typed accompaniment or the assistant's prior answer.
// This reuses ordinary native admission/projection without changing live history.
func (o *OllamaBrain) nativeCompletionContext(ctx context.Context, turns []Turn) ([]api.Message, error) {
	if err := ValidateNativeProjection(turns); err != nil {
		return nil, err
	}
	projection, load := o.audioProjectionLoader(ctx)
	for _, turn := range turns {
		if turn.Role != "user" || turn.Utterance == nil {
			continue
		}
		if _, err := load(turn.Utterance); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if turn.Utterance.Transcript.State != utterance.Ready || turn.Utterance.Transcript.Validate() != nil {
				return nil, fmt.Errorf("completion recording %s: %w", turn.Utterance.ID, ErrAudioProjection)
			}
		}
	}
	messages := make([]api.Message, 0, len(turns))
	for i, turn := range turns {
		message := o.projectTurnWithAudio(turn, turns, i, false, projection)
		// Private completion is tools-free. Historical tool text remains quoted
		// context, never an unmatched tool response or a new call permission.
		switch message.Role {
		case "tool":
			message = api.Message{Role: "user", Content: attributedToolContent(turn.PersonaDisplayName, turn.ToolName, turn.Content)}
		case ollamaSharedRole, ollamaSharedToolRole:
			message.Role = "user"
		case "assistant":
			// The current responder's own historical answer.
		case "user":
			if turn.Utterance != nil && len(message.Images) > 0 {
				message.Content = encodeOllamaUser(turn.Speaker, message.Content)
			}
			message.Content = o.formatStoredUser(message.Content)
		default:
			message = api.Message{Role: "user", Content: fmt.Sprintf("Historical %s context (data):\n%s", turn.Role, turn.Content)}
		}
		message.ToolCalls = nil
		messages = append(messages, message)
	}
	return messages, nil
}
