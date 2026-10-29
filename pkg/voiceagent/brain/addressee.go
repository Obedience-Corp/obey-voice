package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// GroupParticipant carries only public conversational names, never storage IDs.
type GroupParticipant struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

type AddressRequest struct {
	Participants []GroupParticipant `json:"participants"`
	Current      string             `json:"current_responder"`
	Speaker      string             `json:"speaker"`
	Account      string             `json:"account"` // user or agent
	Conversation []AddressMessage   `json:"conversation,omitempty"`
	Text         string             `json:"text"`
	AcousticName string             `json:"acoustic_name,omitempty"`
}

type AddressMessage struct {
	Name    string `json:"name"`
	Account string `json:"account"`
	Text    string `json:"text"`
}

type Addressee struct {
	Kind string `json:"kind"` // none, address, ambiguous
	Name string `json:"name"`
	Bare bool   `json:"bare"`
}

type AddresseeResolver interface {
	ResolveAddressee(context.Context, AddressRequest) (Addressee, error)
}

// ResolveAddressee is private, bounded, tools-free inference. It uses the
// installed responder model without adding classifier input/output to history.
func (o *OllamaBrain) ResolveAddressee(ctx context.Context, req AddressRequest) (Addressee, error) {
	// A correctly transcribed name needs no acoustic disambiguation. Repeating
	// it as an extra hint can bias small classifiers toward false addresses.
	if req.Account != "user" {
		req.AcousticName = ""
	}
	for _, word := range strings.FieldsFunc(req.Text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if strings.EqualFold(word, req.AcousticName) {
			req.AcousticName = ""
			break
		}
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(parentCtx, config.PersonaRoutingTimeoutOf(o.cfg))
	defer cancel()
	payload, err := json.Marshal(req)
	if err != nil {
		return Addressee{}, fmt.Errorf("encode addressee request: %w", err)
	}
	stream := false
	names := addresseeNames(req.Participants)
	format, err := addresseeFormat(names)
	if err != nil {
		return Addressee{}, fmt.Errorf("encode addressee schema: %w", err)
	}
	o.stateMu.Lock()
	options := o.requestOptionsFor(o.contextHighWater)
	model, keepAlive := o.model, o.keepAlive
	o.stateMu.Unlock()
	if options == nil {
		options = map[string]any{}
	}
	options["temperature"], options["seed"], options["num_predict"] = 0, 42, 128
	instruction := systemPrompt(o.cfg, "addressee", nil)
	if req.AcousticName != "" {
		instruction += "\n" + systemPrompt(o.cfg, "acoustic-name", nil)
	}
	request := &api.ChatRequest{Model: model, Messages: []api.Message{{Role: "system", Content: instruction}, {Role: "user", Content: string(payload)}}, Format: format, Stream: &stream, Think: &api.ThinkValue{Value: false}, Options: options, KeepAlive: keepAlive}
	if err := o.messagesBudgetError(request.Messages, nil); err != nil {
		return Addressee{}, fmt.Errorf("addressee context: %w", err)
	}
	var raw strings.Builder
	err = o.client.Chat(ctx, request, func(r api.ChatResponse) error { raw.WriteString(r.Message.Content); return nil })
	if err != nil {
		wrapped := fmt.Errorf("resolve conversation addressee: %w", err)
		if parentErr := parentCtx.Err(); parentErr != nil {
			return Addressee{}, fmt.Errorf("resolve conversation addressee: %w", errors.Join(parentErr, err))
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Addressee{}, admission.WrapClassifierFailure(admission.ClassifierLocalTimeout, wrapped)
		}
		return Addressee{}, admission.WrapClassifierFailure(admission.ClassifierUnavailable, wrapped)
	}
	var result Addressee
	if err := json.Unmarshal([]byte(raw.String()), &result); err != nil {
		return Addressee{}, admission.WrapClassifierFailure(
			admission.ClassifierInvalidOutput,
			fmt.Errorf("invalid addressee decision: %w", err),
		)
	}
	result.Name = strings.TrimSpace(result.Name)
	switch result.Kind {
	case "none":
		if result.Name != "" {
			return Addressee{}, admission.WrapClassifierFailure(admission.ClassifierInvalidOutput, errors.New("invalid no-address decision"))
		}
		// Bare only controls acknowledgements for an addressed persona. A model
		// may label an ordinary greeting bare; without a target it is still a
		// full turn for the current responder, not an invalid identity decision.
		result.Bare = false
	case "address":
		if result.Name == "" {
			return Addressee{}, admission.WrapClassifierFailure(admission.ClassifierInvalidOutput, errors.New("empty addressee"))
		}
		if !addresseeNameAllowed(result.Name, names) {
			// Also enforce membership when a backend ignores the JSON schema.
			return Addressee{Kind: "none"}, nil
		}
	case "ambiguous":
		result.Bare = false
	default:
		return Addressee{}, admission.WrapClassifierFailure(admission.ClassifierInvalidOutput, fmt.Errorf("invalid addressee kind %q", result.Kind))
	}
	if req.Account == "agent" {
		result.Bare = false
	}
	return result, nil
}

// CleanHistoricalAssistantText removes obsolete host attribution from private
// classifier context. The canonical transcript remains unchanged.
func CleanHistoricalAssistantText(text string) string { return stripLegacyHistoricalHeader(text) }
