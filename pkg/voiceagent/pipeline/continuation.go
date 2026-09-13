package pipeline

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

const maxAgentContinuations = 3

type continuationStateKey struct{}
type completionStateKey struct{}

type continuationRequest struct {
	admission ResponseAdmission
}

type completionState struct {
	voice bool
	turn  brain.Turn
	ok    bool
}

func withCompletionState(ctx context.Context, state *completionState) context.Context {
	return context.WithValue(ctx, completionStateKey{}, state)
}

func completionFromContext(ctx context.Context) *completionState {
	state, _ := ctx.Value(completionStateKey{}).(*completionState)
	return state
}

func withContinuation(ctx context.Context, request continuationRequest) context.Context {
	return context.WithValue(ctx, continuationStateKey{}, request)
}

func continuationFromContext(ctx context.Context) (continuationRequest, bool) {
	request, ok := ctx.Value(continuationStateKey{}).(continuationRequest)
	return request, ok
}

func (p *Pipeline) nextHandoff(ctx context.Context, state *completionState, visited []string) (ResponseAdmission, bool, error) {
	if state == nil || !state.ok || p.RouteHandoff == nil || p.HasCapturedInput() {
		return ResponseAdmission{}, false, nil
	}
	if ctx.Err() != nil {
		return ResponseAdmission{}, false, ctx.Err()
	}
	// Handoff classification runs after playback, when a user may already be
	// speaking. Reuse the capture/VAD path, but arm it immediately and remove
	// the playback-only echo tail guard. Join it before the next STT session.
	var watch interruptWatch
	var cancelWatch context.CancelFunc
	if ctrl := p.newInterruptController(); state.voice && ctrl.enabled() {
		watchCtx, cancel := context.WithCancel(ctx)
		cancelWatch = cancel
		ctrl.armDelay = 0
		ctrl.tailGuard = 0
		var armAt atomic.Int64
		armAt.Store(time.Now().UnixNano() - int64(time.Nanosecond))
		watch = ctrl.watchWithDone(watchCtx, &armAt)
	}

	type handoffResult struct {
		admission ResponseAdmission
		accepted  bool
		err       error
	}
	resultCh := make(chan handoffResult, 1)
	classifyCtx, cancelClassify := context.WithCancel(ctx)
	defer cancelClassify()
	go func() {
		admission, accepted, err := p.RouteHandoff(classifyCtx, state.turn, append([]string(nil), visited...))
		resultCh <- handoffResult{admission: admission, accepted: accepted, err: err}
	}()

	var result handoffResult
	interrupted := false
	if watch.requests != nil {
		select {
		case <-watch.requests:
			interrupted = true
			p.keepCapture.Store(true)
			p.clearPreparedCapture()
			cancelClassify()
			result = <-resultCh
		case result = <-resultCh:
		case <-ctx.Done():
			cancelClassify()
			result = <-resultCh
			cancelWatch()
			<-watch.done
			return ResponseAdmission{}, false, ctx.Err()
		}
	} else {
		result = <-resultCh
	}
	cancelClassify()
	if cancelWatch != nil {
		cancelWatch()
		<-watch.done
		// Speech can race the classifier's return. Drain the already-buffered
		// request before accepting a continuation.
		if !interrupted {
			select {
			case <-watch.requests:
				interrupted = true
				p.keepCapture.Store(true)
				p.clearPreparedCapture()
			default:
			}
		}
	}
	if interrupted {
		return ResponseAdmission{}, false, nil
	}
	return result.admission, result.accepted, result.err
}

func personaAlreadyVisited(persona string, visited []string) bool {
	persona = strings.TrimSpace(persona)
	if persona == "" {
		return false
	}
	for _, prior := range visited {
		if strings.EqualFold(persona, prior) {
			return true
		}
	}
	return false
}

func (p *Pipeline) runVoiceWithContinuations(ctx context.Context) (string, error) {
	defer func() {
		if ctx.Err() != nil {
			p.clearPreparedCapture()
		}
	}()
	visited := make([]string, 0, maxAgentContinuations+1)
	var firstInput string
	for responseCount := 0; ; responseCount++ {
		state := &completionState{voice: true}
		turnCtx := withCompletionState(ctx, state)
		var input string
		var err error
		input, err = p.runTurnOnce(turnCtx)
		if responseCount == 0 {
			firstInput = input
		}
		if err != nil {
			return firstInput, err
		}
		if state.ok && state.turn.PersonaID != "" && responseCount == 0 {
			visited = append(visited, state.turn.PersonaID)
		}
		if responseCount >= maxAgentContinuations {
			return firstInput, nil
		}
		admission, accepted, err := p.nextHandoff(ctx, state, visited)
		if err != nil {
			return firstInput, err
		}
		if !accepted {
			return firstInput, nil
		}
		if p.HasCapturedInput() {
			return firstInput, nil
		}
		if personaAlreadyVisited(admission.Envelope.PersonaID, visited) {
			return firstInput, nil
		}
		if admission.Envelope.PersonaID != "" {
			visited = append(visited, admission.Envelope.PersonaID)
		}
		ctx = withContinuation(ctx, continuationRequest{admission: admission})
	}
}

func (p *Pipeline) runTextWithContinuations(ctx context.Context, input string) error {
	visited := make([]string, 0, maxAgentContinuations+1)
	for responseCount := 0; ; responseCount++ {
		state := new(completionState)
		turnCtx := withCompletionState(ctx, state)
		if err := p.runTextTurn(turnCtx, input, false); err != nil {
			return err
		}
		if state.ok && state.turn.PersonaID != "" && responseCount == 0 {
			visited = append(visited, state.turn.PersonaID)
		}
		if responseCount >= maxAgentContinuations {
			return nil
		}
		admission, accepted, err := p.nextHandoff(ctx, state, visited)
		if err != nil {
			return err
		}
		if !accepted {
			return nil
		}
		if p.HasCapturedInput() {
			return nil
		}
		if personaAlreadyVisited(admission.Envelope.PersonaID, visited) {
			return nil
		}
		if admission.Envelope.PersonaID != "" {
			visited = append(visited, admission.Envelope.PersonaID)
		}
		ctx = withContinuation(ctx, continuationRequest{admission: admission})
		input = ""
	}
}

// completeAgentResponse records only a normal, non-degraded, non-interrupted
// response. Recovery and routed paths can append assistant history too, so
// history inspection is intentionally not used as a success signal.
func (p *Pipeline) completeAgentResponse(ctx context.Context, response string, metrics *turnMetrics, interrupted bool) {
	state := completionFromContext(ctx)
	if state == nil || ctx.Err() != nil || interrupted || metrics == nil || metrics.degraded || strings.TrimSpace(response) == "" {
		return
	}
	identity := metrics.responseIdentity()
	state.turn = brain.Turn{Role: "assistant", Content: response}
	if identity != nil {
		state.turn.PersonaID = identity.PersonaID
		state.turn.PersonaDisplayName = identity.PersonaDisplayName
		state.turn.PromptHash = identity.PromptHash
		state.turn.ResponseID = identity.ResponseID
	}
	state.ok = true
}
