package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// ResponseAdmission separates the finalized input from its routed request.
// LocalReply uses normal speech stages without invoking a model.
type ResponseAdmission struct {
	Request    string
	LocalReply string
	Envelope   events.ResponseEnvelope
}

type responseVoices struct{ primary, fallback tts.Provider }
type responseVoicesKey struct{}

func (p *Pipeline) admitInput(ctx context.Context, input string, background bool, metrics *turnMetrics) (context.Context, ResponseAdmission, error) {
	if err := p.commitWorkerReplies(); err != nil {
		return ctx, ResponseAdmission{}, err
	}
	admitted := ResponseAdmission{Request: input}
	if p.AdmitInput != nil {
		var err error
		admitted, err = p.AdmitInput(ctx, input, background)
		if err != nil {
			return ctx, admitted, err
		}
	}
	return p.bindAdmission(ctx, input, admitted, metrics), admitted, nil
}

func (p *Pipeline) bindAdmission(ctx context.Context, input string, admitted ResponseAdmission, metrics *turnMetrics) context.Context {
	metrics.response = admitted.Envelope
	metrics.originalInput = input
	if p.Brain != nil {
		metrics.historyAtAdmission = len(p.Brain.History())
	}
	primary, fallback := p.ttsProviders()
	ctx = context.WithValue(events.WithResponse(ctx, admitted.Envelope), responseVoicesKey{}, responseVoices{primary, fallback})
	p.bindBrainInfo()
	return ctx
}

func (p *Pipeline) responseTTS(ctx context.Context) (tts.Provider, tts.Provider) {
	if voices, ok := ctx.Value(responseVoicesKey{}).(responseVoices); ok {
		return voices.primary, voices.fallback
	}
	return p.ttsProviders()
}

func (m *turnMetrics) responseIdentity() *brain.TurnIdentity {
	if m == nil || m.response.ResponseID == "" {
		return nil
	}
	e := m.response
	return &brain.TurnIdentity{PersonaID: e.PersonaID, PersonaDisplayName: e.DisplayName, PromptHash: e.PromptHash, ResponseID: e.ResponseID}
}

func (p *Pipeline) emitResponse(metrics *turnMetrics, event events.Event) {
	if metrics != nil {
		event = events.Attribute(event, metrics.response)
	}
	p.emit(event)
}

func (p *Pipeline) emitResponseContext(ctx context.Context, event events.Event) {
	p.emit(events.Attribute(event, events.ResponseFromContext(ctx)))
}

func appendResponse(provider brain.Provider, response string, identity *brain.TurnIdentity) error {
	if identity != nil {
		if appender, ok := provider.(brain.IdentityHistoryAppender); ok {
			return appender.AppendAssistantResponseWithIdentity(response, *identity)
		}
		return fmt.Errorf("provider cannot append attributed canonical reply")
	}
	if appender, ok := provider.(brain.HistoryAppender); ok {
		return appender.AppendAssistantResponse(response)
	}
	return fmt.Errorf("provider cannot append canonical reply")
}

func appendContinuityHistory(provider brain.ContinuityTransferProvider, additions []brain.Turn) error {
	snapshot := provider.ContinuitySnapshot()
	canonical := append([]brain.Turn(nil), snapshot.Canonical...)
	working := append([]brain.Turn(nil), snapshot.Working...)
	if snapshot.Working == nil {
		working = append([]brain.Turn(nil), canonical...)
	}
	snapshot.Canonical = append(canonical, additions...)
	snapshot.Working = append(working, additions...)
	return provider.ImportContinuity(snapshot)
}

func (p *Pipeline) completeLocalReply(ctx context.Context, input, reply string, metrics *turnMetrics, turn *turnConductor) error {
	now := time.Now().UTC()
	response := brain.Turn{Role: "assistant", Content: reply, At: now}
	if identity := metrics.responseIdentity(); identity != nil {
		response.PersonaID, response.PersonaDisplayName, response.PromptHash, response.ResponseID = identity.PersonaID, identity.PersonaDisplayName, identity.PromptHash, identity.ResponseID
	}
	additions := []brain.Turn{{Role: "user", Content: input, Speaker: p.currentSpeakerID(), At: now}, response}
	if continuity, ok := p.Brain.(brain.ContinuityTransferProvider); ok {
		if err := appendContinuityHistory(continuity, additions); err != nil {
			return err
		}
	} else {
		projection, ok := p.Brain.(brain.HistoryProjectionProvider)
		if !ok {
			return fmt.Errorf("local acknowledgement requires canonical history projection")
		}
		turns := append(p.Brain.History(), additions...)
		if err := projection.ImportHistory(turns); err != nil {
			return err
		}
	}
	if err := p.speakStandalone(ctx, turn, metrics, reply); err != nil {
		metrics.degraded = true
		p.emit(events.Error{Stage: "tts", Message: err.Error()})
	}
	p.emitResponse(metrics, events.ResponseReady{Response: reply, Degraded: metrics.degraded, Interrupted: ctx.Err() != nil})
	if ctx.Err() != nil {
		turn.finish(TurnInterrupted)
	} else {
		turn.finish(TurnCompleted)
	}
	if p.OnTurn != nil {
		p.OnTurn()
	}
	return ctx.Err()
}
