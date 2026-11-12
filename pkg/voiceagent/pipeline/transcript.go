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

// SessionTranscript is a Transcript that also tracks, per persona, how much
// of the conversation a provider keeping its own session already holds.
type SessionTranscript interface {
	Transcript
	// CatchUp returns what the persona's session may receive and the
	// transcript length it covers, or false when the persona has no cursor.
	CatchUp(personaID string) (brain.SessionCatchUp, int, bool)
	// AppendSeen records a completed request whose catch-up covered the
	// transcript up to covered, and marks its turns seen by the persona.
	AppendSeen(epoch, personaID string, covered int, turns ...brain.Turn) error
	// LiveCatchUp is CatchUp for the selected persona, and its id.
	LiveCatchUp() (string, brain.SessionCatchUp, int, bool)
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

// providerStopTimeout bounds how long a new request waits, unannounced, for
// one an earlier turn stopped before it gives up on it. A variable so tests
// can shorten it.
var providerStopTimeout = providerSettleTimeout

// SetProviderStopTimeoutForTest replaces providerStopTimeout for the duration
// of t, so tests in other packages need not wait it out.
func SetProviderStopTimeoutForTest(t interface {
	Helper()
	Cleanup(func())
}, d time.Duration) {
	t.Helper()
	saved := providerStopTimeout
	providerStopTimeout = d
	t.Cleanup(func() { providerStopTimeout = saved })
}

const providerStoppingInfo = "Waiting for the last reply to finish stopping before answering."

// serialProvider is a provider that cannot take a request while an earlier
// one is still ending on it, as when its session stays locked until then.
type serialProvider interface{ SerialRequests() }

// transcriptRecording records one live provider request in the host
// transcript exactly once: when the provider finishes, or when the turn
// settles it, whichever comes first. For a provider keeping the shared
// history it records nothing itself: once the provider has ended, it tells
// the provider how a request that did not complete ended.
type transcriptRecording struct {
	provider brain.Provider
	ctx      context.Context
	cancel   context.CancelFunc
	// drained closes when the provider request has ended. The next request
	// to the same provider waits for it, so they never share session state.
	drained  chan struct{}
	recorded chan struct{}
	// write records the request. unread is the text the provider sent after
	// the last the turn read; routed marks a route the turn did not follow.
	write func(delivered, unread string, err error, routed bool) *brain.Turn
	// shared is set for a provider keeping the shared history, which may be
	// written only once its request has ended.
	shared bool

	mu       sync.Mutex
	finished bool
	// sending is set while a chunk is offered to the turn; handed signals
	// when it clears, so the record waits to learn whether the turn read it.
	sending   bool
	handed    sync.Cond
	received  strings.Builder
	delivered strings.Builder
	reply     *brain.Turn
	// A routed request is recorded by the routed reply, or by release when
	// the turn never reached it.
	route      string
	routeErr   error
	routeTaken bool
	// abandoned is set once a later request stops waiting for this one;
	// waited once a later request has said it is waiting for it.
	abandoned, waited bool
	// afterSend, set only by tests, runs between a chunk reaching the turn
	// and its record.
	afterSend func(string)
}

// offer hands text to the turn through send and records it if the turn read
// it. The record cannot finish while text is in hand, so text the turn read is
// never missing from it. Every caller of finish other than the forwarder
// cancels first, which ends an offer the turn is not reading.
func (r *transcriptRecording) offer(text string, send func(string) bool) bool {
	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		return false
	}
	r.received.WriteString(text)
	r.sending = true
	r.mu.Unlock()
	sent := send(text)
	if sent && r.afterSend != nil {
		r.afterSend(text)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if sent {
		r.delivered.WriteString(text)
	}
	r.sending = false
	r.handed.Broadcast()
	return sent
}

func (r *transcriptRecording) finish(route string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.sending {
		r.handed.Wait()
	}
	if r.finished {
		return
	}
	r.finished = true
	defer close(r.recorded)
	if routeKindSkipsBrain(routeKindFromBrain(route)) {
		r.route, r.routeErr = route, err
		return
	}
	r.reply = r.write(r.delivered.String(), r.unread(), err, false)
}

// unread is the text received after the last the turn read. What the turn
// read is always the start of what was received.
func (r *transcriptRecording) unread() string {
	return strings.TrimPrefix(r.received.String(), r.delivered.String())
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
	r.reply = r.write(r.delivered.String(), r.unread(), err, true)
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
// in the background; its late result is not recorded again. A provider
// keeping the shared history is waited for longer, since only its end
// records the request; one slower still is recorded when it ends, and the
// next turn waits for that.
func (r *transcriptRecording) settle() {
	if r == nil {
		return
	}
	r.cancel()
	wait := recordingGrace
	if r.shared {
		wait = providerStopTimeout
	}
	grace := time.NewTimer(wait)
	defer grace.Stop()
	select {
	case <-r.recorded:
	case <-grace.C:
		if !r.shared {
			r.finish("", r.ctx.Err())
		}
	}
}

// abandon gives up on a stopped request that is still ending: it stays
// recorded as the turn left it, and nothing it sends later is delivered or
// recorded.
func (r *transcriptRecording) abandon() {
	r.cancel()
	r.finish("", r.ctx.Err())
	r.mu.Lock()
	defer r.mu.Unlock()
	r.abandoned = true
}

// ended reports whether the request has ended or been abandoned.
func (r *transcriptRecording) ended() bool {
	select {
	case <-r.drained:
		return true
	default:
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abandoned
}

// firstWait reports whether this is the first time a later request waits
// past providerStopTimeout for this one.
func (r *transcriptRecording) firstWait() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	first := !r.waited
	r.waited = true
	return first
}

// AwaitSharedHistory waits until requests earlier turns stopped have ended on
// a live provider keeping the shared history, which writes that history
// until they do. A host reads or replaces it only after this.
func (p *Pipeline) AwaitSharedHistory(ctx context.Context) error { return p.awaitSharedHistory(ctx) }

func (p *Pipeline) awaitSharedHistory(ctx context.Context) error {
	if p.Brain == nil || !brain.KeepsSharedHistory(p.Brain) {
		return nil
	}
	return p.awaitProvider(ctx)
}

// awaitProvider waits until requests earlier turns stopped have ended on the
// live provider, so two never share its session. One still ending after
// providerStopTimeout is abandoned and the caller goes ahead. A request still
// in use, or one on a provider that cannot take another until it ends, is
// waited for, said once as a status line. It fails only when ctx ends.
func (p *Pipeline) awaitProvider(ctx context.Context) error {
	pending := p.stillEnding(p.Brain)
	if len(pending) == 0 {
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, providerStopTimeout)
	err := awaitEnded(wait, pending)
	cancel()
	if err == nil || ctx.Err() != nil {
		return ctx.Err()
	}
	_, serial := p.Brain.(serialProvider)
	// A provider keeping the shared history writes it until its request ends.
	serial = serial || brain.KeepsSharedHistory(p.Brain)
	announce := false
	for _, r := range p.stillEnding(p.Brain) {
		if !serial && r.ctx.Err() != nil {
			r.abandon()
		} else if r.firstWait() {
			announce = true
		}
	}
	if announce {
		p.emit(events.Info{Message: providerStoppingInfo})
	}
	return awaitEnded(ctx, p.stillEnding(p.Brain))
}

func awaitEnded(ctx context.Context, requests []*transcriptRecording) error {
	for _, r := range requests {
		select {
		case <-r.drained:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// stillEnding returns provider's recorded requests that have neither ended
// nor been abandoned.
func (p *Pipeline) stillEnding(provider brain.Provider) []*transcriptRecording {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	var pending []*transcriptRecording
	for _, r := range p.requests {
		if r.provider == provider && !r.ended() {
			pending = append(pending, r)
		}
	}
	return pending
}

// awaitRequests waits until provider has no recorded request still ending,
// abandoned or not.
// A provider can be parked and taken up again while one is.
func (p *Pipeline) awaitRequests(ctx context.Context, provider brain.Provider) error {
	p.requestMu.Lock()
	var pending []*transcriptRecording
	for _, r := range p.requests {
		if r.provider == provider {
			pending = append(pending, r)
		}
	}
	p.requestMu.Unlock()
	for _, r := range pending {
		select {
		case <-r.drained:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (p *Pipeline) trackRequest(r *transcriptRecording) {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	p.requests = slices.DeleteFunc(p.requests, func(r *transcriptRecording) bool {
		select {
		case <-r.drained:
			return true
		default:
			return false
		}
	})
	p.requests = append(p.requests, r)
}

// startRecordedStream starts the live provider. When the host owns the
// transcript, the request and the text delivered from it are recorded under
// the response identity. A provider keeping its own session receives the
// turns it has not seen; they count as seen only when the provider itself
// finishes without error, never when an interrupted turn records them. A
// provider keeping the shared history records its own requests; one that
// does not complete is recorded as the turn read it (see recordStopped).
func (p *Pipeline) startRecordedStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, *transcriptRecording, error) {
	t := p.transcriptFor(p.Brain)
	shared := brain.KeepsSharedHistory(p.Brain)
	if t == nil && !shared {
		stream, err := p.startBrainStream(ctx, input, opts)
		return stream, nil, err
	}
	if err := p.awaitProvider(ctx); err != nil {
		return nil, nil, err
	}
	epoch := p.ConversationScope().ConversationEpoch
	started := time.Now().UTC()
	var sessions SessionTranscript
	covered := 0
	if s, ok := t.(SessionTranscript); ok && opts.Identity != nil && brain.KeepsPersonaSession(p.Brain) {
		if catchUp, n, ok := s.CatchUp(opts.Identity.PersonaID); ok {
			opts.CatchUp, sessions, covered = &catchUp, s, n
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	provider := p.Brain
	stream, err := p.startBrainStream(ctx, input, opts)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	write := func(delivered, _ string, err error, _ bool) *brain.Turn {
		turns := requestTurns(input, opts, delivered, err, started)
		if len(turns) == 0 {
			return nil
		}
		var appendErr error
		if sessions != nil && err == nil {
			appendErr = sessions.AppendSeen(epoch, opts.Identity.PersonaID, covered, turns...)
		} else {
			appendErr = t.Append(epoch, turns...)
		}
		if appendErr != nil {
			p.emit(events.Error{Stage: "history", Message: fmt.Sprintf("record reply in conversation transcript: %v", appendErr)})
			return nil
		}
		if last := turns[len(turns)-1]; last.Role == "assistant" {
			return &last
		}
		return nil
	}
	if shared {
		write = func(delivered, unread string, err error, routed bool) *brain.Turn {
			recordStopped(provider, input, opts, delivered, unread, err, routed, started)
			return nil
		}
	}
	rec := &transcriptRecording{provider: provider, ctx: ctx, cancel: cancel, drained: make(chan struct{}), recorded: make(chan struct{}), write: write, shared: shared}
	rec.handed.L = &rec.mu
	p.trackRequest(rec)
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
				rec.offer(v, func(v string) bool { return forward(out, v) })
			case v, ok := <-terminal:
				received, terminal = true, nil
				if ok {
					result, hasResult = v, true
				}
			}
		}
		// Thinking sent after the last text is still the turn's to show.
		for thinks != nil {
			v, ok := <-thinks
			if !ok {
				break
			}
			forward(thinking, v)
		}
		err := result.Err
		switch {
		case ctx.Err() != nil:
			// The turn stopped this request, so what it delivered was cut
			// short whatever the provider reports, in its own terms or not.
			err, result.Recovered = ctx.Err(), false
		case !hasResult:
			err = errors.New("provider ended without a result")
		}
		rec.finish(result.Route, err)
		// The turn ends as the transcript records it.
		result.Err = err
		done <- result
	}()
	return &brain.Stream{Chunks: out, Thinking: thinking, Done: done}, rec, nil
}

// recordStopped tells a provider keeping the shared history, once its request
// has ended, how the request ended when the provider's own record of it does
// not stand: the turn stopped it, it failed, or the turn did not follow the
// route it chose. A request that completed is left as the provider recorded it.
func recordStopped(provider brain.Provider, input string, opts brain.StreamOptions, delivered, unread string, err error, routed bool, started time.Time) {
	recorder, ok := provider.(brain.StoppedRequestRecorder)
	if !ok || (err == nil && !routed) || opts.TransientHistory {
		return
	}
	stopped := brain.StoppedRequest{Unread: unread}
	for _, turn := range requestTurns(input, opts, delivered, err, started) {
		switch {
		case turn.Role == "assistant":
			stopped.Reply = &turn
		case opts.Discussion == nil:
			// A discussion's line was recorded when the discussion began.
			stopped.Line = &turn
		}
	}
	recorder.RecordStoppedRequest(stopped)
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
