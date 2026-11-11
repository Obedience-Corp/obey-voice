package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Transcript is the host-owned canonical record of a conversation. Appends
// carry the conversation epoch their turn began in; an implementation refuses
// turns from a conversation that is no longer current. Clear empties it when
// the user clears the conversation.
type Transcript interface {
	Turns() []brain.Turn
	Append(epoch string, turns ...brain.Turn) error
	Clear()
	// SetRecordedReplyMedia moves a reply's images into reply, a reply this
	// transcript recorded, and into no other.
	SetRecordedReplyMedia(reply brain.Turn, text string, refs []chatmedia.Ref) error
}

// transcriptFor returns the host transcript when provider does not keep the
// shared history itself.
func (p *Pipeline) transcriptFor(provider brain.Provider) Transcript {
	if p.Transcript == nil || provider == nil || brain.KeepsSharedHistory(provider) {
		return nil
	}
	return p.Transcript
}

func (p *Pipeline) appendTranscript(t Transcript, turns ...brain.Turn) error {
	return t.Append(p.ConversationScope().ConversationEpoch, turns...)
}

func assistantTurn(text string, identity *brain.TurnIdentity, at time.Time) brain.Turn {
	t := brain.Turn{Role: "assistant", Content: strings.TrimSpace(text), At: at}
	if identity != nil {
		t.PersonaID, t.PersonaDisplayName, t.PromptHash, t.ResponseID = identity.PersonaID, identity.PersonaDisplayName, identity.PromptHash, identity.ResponseID
	}
	return t
}

// appendReply commits a host-authored reply to the provider and, when the
// host owns the transcript, to the transcript as well.
func (p *Pipeline) appendReply(provider brain.Provider, response string, identity *brain.TurnIdentity, attachments []chatmedia.Ref) error {
	if err := appendResponse(provider, response, identity); err != nil {
		return err
	}
	t := p.transcriptFor(provider)
	if t == nil {
		return nil
	}
	reply := assistantTurn(response, identity, time.Now().UTC())
	reply.Attachments = slices.Clone(attachments)
	return p.appendTranscript(t, reply)
}

const (
	// recordingGrace bounds how long a turn that stopped reading waits for the
	// provider's own result before it records what was delivered.
	recordingGrace = 100 * time.Millisecond
	// providerSettleTimeout bounds how long a clear waits for a stopped
	// provider request to end before it resets the provider.
	providerSettleTimeout = 5 * time.Second
)

// providerStopTimeout bounds how long a new turn waits for a request an
// earlier turn stopped. A variable so tests can shorten it.
var providerStopTimeout = providerSettleTimeout

var errProviderStopping = errors.New("the last reply is still stopping; try again in a moment")

// transcriptRecording records one live provider request in the host
// transcript exactly once: when the provider finishes, or when the turn
// settles it, whichever comes first.
type transcriptRecording struct {
	provider brain.Provider
	ctx      context.Context
	cancel   context.CancelFunc
	// drained closes when the provider request has ended. The next request
	// to the same provider waits for it, so they never share session state.
	drained  chan struct{}
	recorded chan struct{}
	write    func(delivered string, err error) *brain.Turn

	mu        sync.Mutex
	finished  bool
	delivered strings.Builder
	reply     *brain.Turn
	// A routed request is recorded by the routed reply, or by release when
	// the turn never reached it.
	route      string
	routeErr   error
	routeTaken bool
}

func (r *transcriptRecording) deliver(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finished {
		r.delivered.WriteString(text)
	}
}

func (r *transcriptRecording) finish(route string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	defer close(r.recorded)
	if routeKindSkipsBrain(routeKindFromBrain(route)) {
		r.route, r.routeErr = route, err
		return
	}
	r.reply = r.write(r.delivered.String(), err)
}

// takeRoute hands the request's record to the routed reply.
func (r *transcriptRecording) takeRoute() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routeTaken = true
}

// release settles the request and records a routed one whose routed reply
// never ran, so the user's line is kept.
func (r *transcriptRecording) release() {
	if r == nil {
		return
	}
	r.settle()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.route == "" || r.routeTaken {
		return
	}
	r.routeTaken = true
	err := r.routeErr
	if err == nil {
		err = r.ctx.Err()
	}
	r.reply = r.write(r.delivered.String(), err)
}

// recordedReply is the reply this request recorded, if any.
func (r *transcriptRecording) recordedReply() *brain.Turn {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reply
}

// settle stops a request the turn no longer reads and makes sure it is
// recorded before the turn returns. A provider that is slow to stop finishes
// in the background; its late result is not recorded again.
func (r *transcriptRecording) settle() {
	if r == nil {
		return
	}
	r.cancel()
	grace := time.NewTimer(recordingGrace)
	defer grace.Stop()
	select {
	case <-r.recorded:
	case <-grace.C:
		r.finish("", r.ctx.Err())
	}
}

// awaitProvider waits, for at most providerStopTimeout, until a request that
// an earlier turn stopped has ended on the live provider.
func (p *Pipeline) awaitProvider(ctx context.Context) error {
	wait, cancel := context.WithTimeout(ctx, providerStopTimeout)
	defer cancel()
	err := p.settleProvider(wait)
	if err != nil && ctx.Err() == nil {
		p.emit(events.Error{Stage: "brain", Message: errProviderStopping.Error()})
		return errProviderStopping
	}
	return err
}

// settleProvider waits until a request that an earlier turn stopped has
// ended on the live provider.
func (p *Pipeline) settleProvider(ctx context.Context) error {
	p.requestMu.Lock()
	r := p.lastRequest
	p.requestMu.Unlock()
	if r == nil || r.provider != p.Brain {
		return nil
	}
	select {
	case <-r.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startRecordedStream starts the live provider. When the host owns the
// transcript, the request and the text delivered from it are recorded under
// the response identity.
func (p *Pipeline) startRecordedStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, *transcriptRecording, error) {
	t := p.transcriptFor(p.Brain)
	if t == nil {
		stream, err := p.startBrainStream(ctx, input, opts)
		return stream, nil, err
	}
	if err := p.awaitProvider(ctx); err != nil {
		return nil, nil, err
	}
	epoch := p.ConversationScope().ConversationEpoch
	started := time.Now().UTC()
	ctx, cancel := context.WithCancel(ctx)
	provider := p.Brain
	stream, err := p.startBrainStream(ctx, input, opts)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	rec := &transcriptRecording{provider: provider, ctx: ctx, cancel: cancel, drained: make(chan struct{}), recorded: make(chan struct{}),
		write: func(delivered string, err error) *brain.Turn {
			turns := requestTurns(input, opts, delivered, err, started)
			if len(turns) == 0 {
				return nil
			}
			if appendErr := t.Append(epoch, turns...); appendErr != nil {
				p.emit(events.Error{Stage: "history", Message: fmt.Sprintf("record reply in conversation transcript: %v", appendErr)})
				return nil
			}
			if last := turns[len(turns)-1]; last.Role == "assistant" {
				return &last
			}
			return nil
		}}
	p.requestMu.Lock()
	p.lastRequest = rec
	p.requestMu.Unlock()
	// Unbuffered, so text counts as delivered only once the turn has read it.
	out := make(chan string)
	var thinking chan string
	if stream.Thinking != nil {
		thinking = make(chan string, 8)
	}
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(rec.drained)
		defer close(done)
		if thinking != nil {
			defer close(thinking)
		}
		defer close(out)
		forwarding := true
		forward := func(ch chan<- string, v string) bool {
			if !forwarding || ctx.Err() != nil {
				forwarding = false
				return false
			}
			select {
			case ch <- v:
				return true
			case <-ctx.Done():
				forwarding = false
				return false
			}
		}
		chunks, thinks, terminal := stream.Chunks, stream.Thinking, stream.Done
		var result brain.StreamResult
		received, hasResult := false, false
		for chunks != nil || !received {
			select {
			case v, ok := <-thinks:
				if !ok {
					thinks = nil
					continue
				}
				forward(thinking, v)
			case v, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				if forward(out, v) {
					rec.deliver(v)
				}
			case v, ok := <-terminal:
				received, terminal = true, nil
				if ok {
					result, hasResult = v, true
				}
			}
		}
		err := result.Err
		switch {
		case !hasResult:
			err = errors.New("provider ended without a result")
		case ctx.Err() != nil:
			// The turn stopped this request, so what it delivered was cut
			// short whatever the provider reports, in its own terms or not.
			err = ctx.Err()
		}
		rec.finish(result.Route, err)
		// The turn ends as the transcript records it.
		if result.Err == nil {
			result.Err = err
		}
		done <- result
	}()
	return &brain.Stream{Chunks: out, Thinking: thinking, Done: done}, rec, nil
}

// requestTurns follows the shared-history providers: the finalized input is
// the user's line, a host continuation adds none, the reply is stored in the
// same cleaned form providers store, and unfinished text keeps its disposition.
func requestTurns(input string, opts brain.StreamOptions, delivered string, err error, started time.Time) []brain.Turn {
	var turns []brain.Turn
	if !opts.AgentContinuation && !opts.RetryInput && !opts.Background {
		user := brain.Turn{Role: "user", Content: input, Speaker: opts.Speaker, At: started, Attachments: slices.Clone(opts.Attachments)}
		if opts.OriginalInput != "" {
			user.Content = opts.OriginalInput
			if input != "" && input != opts.OriginalInput {
				user.RoutedInput = input
			}
		}
		if strings.TrimSpace(user.Content) != "" || len(user.Attachments) > 0 {
			turns = append(turns, user)
		}
	}
	if reply := brain.CleanForVoice(delivered); strings.TrimSpace(reply) != "" {
		t := assistantTurn(reply, opts.Identity, time.Now().UTC())
		if err != nil {
			t.Disposition = brain.IncompleteDisposition(err)
		}
		turns = append(turns, t)
	}
	return turns
}
