package personaruntime

import (
	"context"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// classifyAddressee resolves a candidate address with the decision model first
// and the provider classifier second. Neither is a harness backend: the
// classifier exists only when the live brain is Ollama. It returns nil when no
// semantic answer exists, leaving the current persona (or the legacy grammar).
func (s *Selector) classifyAddressee(ctx context.Context, resolver brain.AddresseeResolver, viaDecision bool, request brain.AddressRequest) (*brain.Addressee, error) {
	if viaDecision {
		request = request.WithoutRedundantAcousticName()
		answer, ok, err := s.opts.Pipeline.Decisions.ResolveAddressee(ctx, decisionAddresseeInput(request), s.addresseeGuidance(request))
		switch {
		case err == nil && ok:
			return &brain.Addressee{Kind: answer.Kind, Name: answer.Name, Bare: answer.Bare}, nil
		case err != nil && (resolver == nil || ctx.Err() != nil):
			return nil, err
		}
	}
	if resolver == nil {
		return nil, nil
	}
	answer, err := resolver.ResolveAddressee(ctx, request)
	if err != nil {
		return nil, err
	}
	return &answer, nil
}

func (s *Selector) addresseeGuidance(request brain.AddressRequest) string {
	dir := ""
	if s.opts.Base != nil {
		dir = config.PromptsDirFrom(s.opts.Base)
	}
	text, err := prompts.RenderSystem(dir, "addressee", nil)
	if err != nil {
		return ""
	}
	if request.AcousticName != "" {
		if extra, err := prompts.RenderSystem(dir, "acoustic-name", nil); err == nil {
			text += "\n" + extra
		}
	}
	return strings.TrimSpace(text)
}

func decisionAddresseeInput(request brain.AddressRequest) decision.AddresseeInput {
	in := decision.AddresseeInput{
		Participants: make([]decision.AddresseeParticipant, len(request.Participants)),
		Current:      request.Current, Speaker: request.Speaker, Account: request.Account,
		Text: request.Text, AcousticName: request.AcousticName,
	}
	for i, p := range request.Participants {
		in.Participants[i] = decision.AddresseeParticipant{Name: p.Name, Aliases: p.Aliases}
	}
	for _, m := range request.Conversation {
		in.Conversation = append(in.Conversation, decision.AddresseeMessage{Name: m.Name, Account: m.Account, Text: m.Text})
	}
	return in
}
