package personaruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// Resolver names reported by the addressee diagnostic.
const (
	resolvedByDecision   = "decision_model"
	resolvedByClassifier = "ollama_classifier"
	resolvedByGrammar    = "grammar"
	resolvedByCurrent    = "current_persona"
	resolutionFailed     = "failed"
)

type addresseeTrace struct {
	resolver, disposition, model string
}

// classifyAddressee resolves a candidate address with the decision model first
// and the provider classifier second. Neither is a harness backend: the
// classifier exists only when the live brain is Ollama. It returns nil when no
// semantic answer exists, leaving the current persona (or the legacy grammar).
// A decision-model failure with no classifier behind it also returns nil, with
// a notice for a user message; only the turn's own cancellation fails here.
func (s *Selector) classifyAddressee(ctx context.Context, resolver brain.AddresseeResolver, viaDecision bool, request brain.AddressRequest, trace *addresseeTrace) (*brain.Addressee, error) {
	trace.disposition = "not_asked"
	bounded := ctx
	if viaDecision {
		// persona_routing_timeout bounds the whole question, so the decision
		// budget cannot be added on top of the classifier's own timeout.
		var cancel context.CancelFunc
		bounded, cancel = context.WithTimeout(ctx, config.PersonaRoutingTimeoutOf(s.opts.Base))
		defer cancel()
		request = request.WithoutRedundantAcousticName()
		result, err := s.opts.Pipeline.Decisions.ResolveAddressee(bounded, decisionAddresseeInput(request), s.addresseeGuidance(request))
		trace.disposition, trace.model = string(result.Outcome), result.Model
		switch {
		case err == nil && result.Answered():
			trace.resolver = resolvedByDecision
			answer := result.Addressee
			return &brain.Addressee{Kind: answer.Kind, Name: answer.Name, Bare: answer.Bare}, nil
		case err != nil && ctx.Err() != nil:
			return nil, err
		case err != nil && resolver == nil:
			// A persona's reply has no user waiting on it; the diagnostic
			// records that failure, so a turn gets at most one notice.
			if s.opts.Pipeline.Events != nil && request.Account == "user" {
				s.opts.Pipeline.Events.Emit(events.Info{Message: fmt.Sprintf("The decision model did not answer who was addressed (%s); routing by name only.", result.Outcome)})
			}
		}
	}
	if resolver == nil {
		return nil, nil
	}
	answer, err := resolver.ResolveAddressee(bounded, request)
	if err != nil {
		if _, classified := admission.ClassifierFailureKindOf(err); !classified && ctx.Err() == nil && bounded.Err() != nil {
			err = admission.WrapClassifierFailure(admission.ClassifierLocalTimeout, err)
		}
		return nil, err
	}
	trace.resolver = resolvedByClassifier
	return &answer, nil
}

func (s *Selector) emitAddresseeTrace(trace addresseeTrace, started time.Time) {
	if s.opts.Pipeline.Events != nil {
		s.opts.Pipeline.Events.Emit(events.DecisionEvaluated{Point: string(decision.AddresseeResolution), Disposition: trace.disposition, Resolver: trace.resolver, Model: trace.model, Elapsed: time.Since(started)})
	}
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
