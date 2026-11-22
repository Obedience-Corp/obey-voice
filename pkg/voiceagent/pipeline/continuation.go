package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// HandoffProposal is a side-effect-free addressing decision. Commit runs once,
// at the serialized response boundary after pending user speech has priority.
// A failed Commit must leave the previous selection intact.
type HandoffProposal struct {
	DiscussionID string
	OpenAddition bool
	PersonaID    string
	// Invited are the personas the reply asked to answer after PersonaID, in
	// that order. Each takes the floor in turn before a reply is classified
	// for a new handoff.
	Invited []HandoffInvite
	Commit  func(context.Context) (ResponseAdmission, error)
}

// HandoffInvite is a persona a reply invited to answer after another.
// InvitedBy is the ResponseID of that reply.
type HandoffInvite struct {
	PersonaID   string
	DisplayName string
	InvitedBy   string
}

// InviteRefusal is an InviteHandoff error that no waiting invitee can take
// the floor, so the invitation ends with one notice naming them all.
type InviteRefusal struct{ Reason string }

func (e *InviteRefusal) Error() string { return e.Reason }

type handoffOutcome uint8

const (
	// handoffDeclined leaves the reply with its speaker: no proposal, a
	// refused one, or a failed classifier or commit.
	handoffDeclined handoffOutcome = iota
	handoffAccepted
	// handoffInterrupted means the user spoke; the turn ends with no handoff.
	handoffInterrupted
)

const maxAgentContinuations = 3

type continuationStateKey struct{}
type completionStateKey struct{}

type continuationRequest struct {
	admission ResponseAdmission
}

type completionState struct {
	trace      events.TraceContext
	owner      context.Context
	discussion *discussion.Directive
	voice      bool
	turn       brain.Turn
	ok         bool
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

// nextHandoff chooses who answers the reply that just finished. A persona
// still waiting in invited goes first, without classifying the reply; once
// none is left, the reply is classified. It returns the invitees still waiting
// after the one admitted.
func (p *Pipeline) nextHandoff(ctx context.Context, state *completionState, visited []string, invited []HandoffInvite) (ResponseAdmission, []HandoffInvite, bool, error) {
	if (state != nil && state.discussion != nil) || p.InviteHandoff == nil {
		invited = nil
	}
	if state == nil || !state.ok {
		p.reportUninvited(ctx, invited, visited, "the reply before theirs did not finish")
		return ResponseAdmission{}, nil, false, nil
	}
	if (p.RouteHandoff == nil && len(invited) == 0) || p.HasCapturedInput() {
		return ResponseAdmission{}, nil, false, nil
	}
	if ctx.Err() != nil {
		return ResponseAdmission{}, nil, false, ctx.Err()
	}
	for len(invited) > 0 {
		invite := invited[0]
		invited = invited[1:]
		if personaAlreadyVisited(invite.PersonaID, visited) {
			continue
		}
		var refusal *InviteRefusal
		admission, _, outcome, err := p.awaitHandoff(ctx, state, visited, func(ctx context.Context) (HandoffProposal, bool, error) {
			proposal, accepted, err := p.InviteHandoff(ctx, state.turn, invite, append([]string(nil), visited...))
			if errors.As(err, &refusal) {
				return HandoffProposal{}, false, nil
			}
			return proposal, accepted, err
		})
		switch {
		case err != nil:
			return ResponseAdmission{}, nil, false, err
		case outcome == handoffAccepted:
			return admission, invited, true, nil
		case outcome == handoffInterrupted:
			return ResponseAdmission{}, nil, false, nil
		case refusal != nil:
			p.reportUninvited(ctx, append([]HandoffInvite{invite}, invited...), visited, refusal.Reason)
			return ResponseAdmission{}, nil, false, nil
		}
	}
	if p.RouteHandoff == nil {
		return ResponseAdmission{}, nil, false, nil
	}
	admission, proposal, outcome, err := p.awaitHandoff(ctx, state, visited, func(ctx context.Context) (HandoffProposal, bool, error) {
		return p.RouteHandoff(ctx, state.turn, append([]string(nil), visited...))
	})
	if err != nil || outcome != handoffAccepted {
		return ResponseAdmission{}, nil, false, err
	}
	if proposal.DiscussionID != "" {
		return admission, nil, true, nil
	}
	return admission, proposal.Invited, true, nil
}

// awaitHandoff runs propose and commits its proposal at the response boundary.
// User speech during either one ends the turn instead.
func (p *Pipeline) awaitHandoff(ctx context.Context, state *completionState, visited []string, propose func(context.Context) (HandoffProposal, bool, error)) (ResponseAdmission, HandoffProposal, handoffOutcome, error) {
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
		proposal HandoffProposal
		accepted bool
		err      error
	}
	resultCh := make(chan handoffResult, 1)
	// The classifier reads the reply that just finished, so its requests
	// belong to that turn. Only the classifier sees this trace.
	classifyCtx, cancelClassify := context.WithCancel(events.WithTrace(ctx, state.trace))
	defer cancelClassify()
	go func() {
		proposal, accepted, err := propose(withCompletionState(classifyCtx, state))
		resultCh <- handoffResult{proposal: proposal, accepted: accepted, err: err}
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
			return ResponseAdmission{}, HandoffProposal{}, handoffDeclined, ctx.Err()
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
		return ResponseAdmission{}, HandoffProposal{}, handoffInterrupted, nil
	}
	if err := ctx.Err(); err != nil {
		return ResponseAdmission{}, HandoffProposal{}, handoffDeclined, err
	}
	if result.err != nil {
		p.emit(events.Info{Message: fmt.Sprintf("Agent handoff skipped: %v", result.err)})
		return ResponseAdmission{}, HandoffProposal{}, handoffDeclined, nil
	}
	if p.HasCapturedInput() {
		return ResponseAdmission{}, HandoffProposal{}, handoffInterrupted, nil
	}
	proposal := result.proposal
	groupProposal := proposal.DiscussionID != "" && ((state.discussion != nil && state.discussion.ID == proposal.DiscussionID) || proposal.OpenAddition)
	if !result.accepted || proposal.Commit == nil || proposal.PersonaID == "" || (!groupProposal && personaAlreadyVisited(proposal.PersonaID, visited)) || (proposal.DiscussionID != "" && !groupProposal) {
		return ResponseAdmission{}, HandoffProposal{}, handoffDeclined, nil
	}
	admission, err := proposal.Commit(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ResponseAdmission{}, HandoffProposal{}, handoffDeclined, ctx.Err()
		}
		p.emit(events.Info{Message: fmt.Sprintf("Agent handoff skipped: %v", err)})
		return ResponseAdmission{}, HandoffProposal{}, handoffDeclined, nil
	}
	return admission, proposal, handoffAccepted, nil
}

// reportUninvited names the invitees left without a turn and why. User speech
// or a cancelled turn ends the invitation without notice.
func (p *Pipeline) reportUninvited(ctx context.Context, invited []HandoffInvite, visited []string, reason string) {
	if ctx.Err() != nil || p.HasCapturedInput() {
		return
	}
	var names []string
	for _, invite := range invited {
		if personaAlreadyVisited(invite.PersonaID, visited) {
			continue
		}
		name := strings.TrimSpace(invite.DisplayName)
		if name == "" {
			name = invite.PersonaID
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return
	}
	list := names[0]
	if len(names) > 1 {
		list = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	verb := "was"
	if len(names) > 1 {
		verb = "were"
	}
	p.emit(events.Info{Message: fmt.Sprintf("%s %s invited but did not get a turn: %s.", list, verb, reason)})
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
	var last *completionState
	defer func() {
		if p.EndDiscussion != nil {
			reason := "yielded"
			if ctx.Err() != nil {
				reason = "interrupted"
			} else if last != nil && last.discussion != nil && !last.ok {
				reason = "failed"
			}
			p.EndDiscussion(reason)
		}
	}()
	defer func() {
		if ctx.Err() != nil {
			p.clearPreparedCapture()
		}
	}()
	visited := make([]string, 0, maxAgentContinuations+1)
	var invited []HandoffInvite
	var firstInput string
	for responseCount := 0; ; responseCount++ {
		state := &completionState{voice: true, owner: ctx}
		last = state
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
		if state.ok && state.discussion != nil && p.CompleteDiscussion != nil {
			if err := p.CompleteDiscussion(ctx, state.turn); err != nil {
				return firstInput, err
			}
		}
		if state.ok && state.turn.PersonaID != "" && responseCount == 0 {
			visited = append(visited, state.turn.PersonaID)
		}
		if responseCount >= continuationLimit(state) {
			p.reportUninvited(ctx, invited, visited, fmt.Sprintf("a message allows %d handoffs", maxAgentContinuations))
			if state.discussion != nil && p.EndDiscussion != nil {
				snap := state.discussion.State.Snapshot()
				reason := "budget_exhausted"
				if snap.Mode == "open" {
					reason = "open_complete"
				} else if len(snap.Members) == 1 {
					reason = "yielded"
				}
				p.EndDiscussion(reason)
			}
			return firstInput, nil
		}
		admission, waiting, accepted, err := p.nextHandoff(ctx, state, visited, invited)
		if err != nil {
			return firstInput, err
		}
		if !accepted {
			return firstInput, nil
		}
		invited = waiting
		if admission.Envelope.PersonaID != "" {
			visited = append(visited, admission.Envelope.PersonaID)
		}
		ctx = withContinuation(events.WithTrace(ctx, state.trace), continuationRequest{admission: admission})
	}
}

func (p *Pipeline) runTextWithContinuations(ctx context.Context, input string) error {
	var last *completionState
	defer func() {
		if p.EndDiscussion != nil {
			reason := "yielded"
			if ctx.Err() != nil {
				reason = "interrupted"
			} else if last != nil && last.discussion != nil && !last.ok {
				reason = "failed"
			}
			p.EndDiscussion(reason)
		}
	}()
	visited := make([]string, 0, maxAgentContinuations+1)
	var invited []HandoffInvite
	for responseCount := 0; ; responseCount++ {
		state := &completionState{owner: ctx}
		last = state
		turnCtx := withCompletionState(ctx, state)
		if err := p.runTextTurn(turnCtx, input, false); err != nil {
			return err
		}
		if state.ok && state.discussion != nil && p.CompleteDiscussion != nil {
			if err := p.CompleteDiscussion(ctx, state.turn); err != nil {
				return err
			}
		}
		if state.ok && state.turn.PersonaID != "" && responseCount == 0 {
			visited = append(visited, state.turn.PersonaID)
		}
		if responseCount >= continuationLimit(state) {
			p.reportUninvited(ctx, invited, visited, fmt.Sprintf("a message allows %d handoffs", maxAgentContinuations))
			if state.discussion != nil && p.EndDiscussion != nil {
				snap := state.discussion.State.Snapshot()
				reason := "budget_exhausted"
				if snap.Mode == "open" {
					reason = "open_complete"
				} else if len(snap.Members) == 1 {
					reason = "yielded"
				}
				p.EndDiscussion(reason)
			}
			return nil
		}
		admission, waiting, accepted, err := p.nextHandoff(ctx, state, visited, invited)
		if err != nil {
			return err
		}
		if !accepted {
			return nil
		}
		invited = waiting
		if admission.Envelope.PersonaID != "" {
			visited = append(visited, admission.Envelope.PersonaID)
		}
		ctx = withContinuation(events.WithTrace(ctx, state.trace), continuationRequest{admission: admission})
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
	if state.voice && state.discussion != nil && metrics.ttfa.IsZero() {
		return
	}
	identity := metrics.responseIdentity()
	reply := p.finalizeMedia(metrics, response)
	state.turn = brain.Turn{Role: "assistant", Content: reply.Response, Attachments: reply.Attachments}
	if identity != nil {
		state.turn.PersonaID = identity.PersonaID
		state.turn.PersonaDisplayName = identity.PersonaDisplayName
		state.turn.PromptHash = identity.PromptHash
		state.turn.ResponseID = identity.ResponseID
	}
	state.trace = metrics.trace
	state.ok = true
}

func continuationLimit(state *completionState) int {
	if state.discussion == nil {
		return maxAgentContinuations
	}
	snap := state.discussion.State.Snapshot()
	if snap.Mode == "open" || len(snap.Members) == 1 {
		return 0
	}
	return min(discussion.MaxResponses, 2*len(snap.Members)) - 1
}

// ContinuationOwner survives cancellation of an individual private classifier.
func ContinuationOwner(ctx context.Context) context.Context {
	if state := completionFromContext(ctx); state != nil && state.owner != nil {
		return state.owner
	}
	return ctx
}
