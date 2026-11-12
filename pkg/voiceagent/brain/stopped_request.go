package brain

import (
	"log/slog"
	"strings"

	"github.com/ollama/ollama/api"
)

// StoppedRequest is how a streamed request to a provider keeping the shared
// history ended when the provider's own record of it does not stand: the turn
// stopped it, it failed, or it chose a route the turn did not follow.
type StoppedRequest struct {
	// Line is the user's line of the request, or nil when it carried none.
	Line *Turn
	// Reply is the text the turn read, recorded as an unfinished reply under
	// the request's identity, or nil when the turn read none.
	Reply *Turn
	// Unread is the text the provider sent after the last the turn read.
	Unread string
}

// StoppedRequestRecorder is a provider keeping the shared history that the
// host tells how a request ended when the provider's own record of it does
// not stand, once the request has ended. The provider records the request as
// the turn left it, in place of what it recorded of it itself.
type StoppedRequestRecorder interface {
	RecordStoppedRequest(StoppedRequest)
}

// RequestAbandoner is a StoppedRequestRecorder the host tells when it stops
// waiting for a stopped request that is still ending. Nothing that request
// does when it ends changes the conversation, and the next request does not
// share its session.
type RequestAbandoner interface {
	AbandonRequest()
}

// streamedRequest marks the last request a provider replaying the shared
// history was given: where it began in canonical and the record the provider
// keeps of it when it completes. The provider forgets it when the conversation
// is replaced or the host abandons the request, so a late record of that
// request changes nothing.
type streamedRequest struct {
	start int
	// records is false when the provider keeps no record of the request, as
	// when a route answered it. Otherwise the record is the user line, when
	// hasLine, then the reply under responseID.
	records    bool
	hasLine    bool
	line       string
	responseID string
}

func newStreamedRequest(canonical []Turn, input string, opts StreamOptions) *streamedRequest {
	r := &streamedRequest{start: len(canonical), records: !opts.TransientHistory, hasLine: !opts.AgentContinuation, line: input}
	if opts.OriginalInput != "" {
		r.line = opts.OriginalInput
	}
	if opts.Identity != nil {
		r.responseID = opts.Identity.ResponseID
	}
	return r
}

// routedRequest marks a request a route answered before the provider saw it.
func routedRequest(canonical []Turn) *streamedRequest {
	return &streamedRequest{start: len(canonical)}
}

// ownRecord reports how many turns at the end of canonical are the record
// the provider kept of r, or false when canonical no longer ends with that
// record: the conversation changed after r began.
func (r *streamedRequest) ownRecord(canonical []Turn) (int, bool) {
	if r == nil || r.start > len(canonical) {
		return 0, false
	}
	n := len(canonical) - r.start
	if n == 0 {
		return 0, true
	}
	want := 1
	if r.hasLine {
		want = 2
	}
	first, reply := canonical[r.start], canonical[len(canonical)-1]
	if !r.records || n != want || (r.hasLine && (first.Role != "user" || first.Content != r.line)) ||
		reply.Role != "assistant" || reply.ResponseID != r.responseID {
		return 0, false
	}
	return n, true
}

// replaceStoppedRequest takes the record the provider kept of r, if any, from
// the end of canonical and working, and puts the stopped request in its
// place. It changes nothing and reports false when canonical no longer ends
// with that record.
func replaceStoppedRequest(canonical, working []Turn, r *streamedRequest, s StoppedRequest) ([]Turn, []Turn, bool) {
	recorded, ok := r.ownRecord(canonical)
	if !ok {
		if r != nil {
			slog.Warn("stopped request not recorded: the conversation changed after it began", "began_at", r.start, "turns", len(canonical))
		}
		return canonical, working, false
	}
	canonical = canonical[:len(canonical)-recorded]
	working = working[:len(working)-min(recorded, len(working))]
	for _, t := range []*Turn{s.Line, s.Reply} {
		if t != nil {
			canonical = append(canonical, deepCopyTurns([]Turn{*t})...)
			working = append(working, deepCopyTurns([]Turn{*t})...)
		}
	}
	return canonical, working, true
}

func (c *CodexBrain) RecordStoppedRequest(s StoppedRequest) {
	r := c.request
	c.request = nil
	if r == nil {
		return
	}
	if canonical, working, ok := replaceStoppedRequest(c.canonical, c.working, r, s); ok {
		c.canonical, c.working = canonical, working
		c.trimWorking()
	}
	// The thread may hold more of the reply than the turn read.
	c.dropThread()
}

func (c *CodexBrain) AbandonRequest() {
	c.request = nil
	// The abandoned turn may still be running on the thread.
	c.dropThread()
}

// owns reports whether r is still the request whose end may change the
// conversation; nil is a request outside that, as ThinkFull's.
func (c *CodexBrain) owns(r *streamedRequest) bool { return r == nil || c.request == r }

func (b *MuseSparkBrain) RecordStoppedRequest(s StoppedRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.request
	b.request = nil
	if canonical, working, ok := replaceStoppedRequest(b.canonical, b.working, r, s); ok {
		b.canonical, b.working = canonical, working
		b.trimWorking()
	}
}

func (b *MuseSparkBrain) AbandonRequest() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.request = nil
}

func (b *ObeyBrain) RecordStoppedRequest(s StoppedRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.request
	b.request = nil
	if canonical, working, ok := replaceStoppedRequest(b.canonical, b.working, r, s); ok {
		b.canonical, b.working = canonical, working
		b.trimWorking()
	}
}

func (b *ObeyBrain) AbandonRequest() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.request = nil
}

// ollamaStopped is what the last streamed request left that the host may
// correct once the turn stopped it: whether its last turn is the reply it
// recorded, the text it sent for that reply, and a user line a route took
// out of the history.
type ollamaStopped struct {
	reply  bool
	sent   string
	routed *ollamaRoutedLine
}

type ollamaRoutedLine struct {
	canonical, working Turn
	message            api.Message
}

// RecordStoppedRequest keeps the reply as the turn read it, without text the
// turn never read, marked unfinished, and drops it when the turn read none of
// it. A user line a route took out goes back when the route was not followed.
func (o *OllamaBrain) RecordStoppedRequest(s StoppedRequest) {
	stopped := o.stopped
	o.stopped = ollamaStopped{}
	if r := stopped.routed; r != nil {
		if s.Line != nil {
			o.canonical = append(o.canonical, r.canonical)
			o.working = append(o.working, r.working)
			o.history = append(o.history, r.message)
		}
		return
	}
	last := len(o.canonical) - 1
	if !stopped.reply || last < 0 || len(o.working) == 0 || len(o.history) == 0 {
		return
	}
	reply := o.canonical[last]
	recorded := reply
	switch {
	case s.Unread == "":
	case strings.HasSuffix(stopped.sent, s.Unread):
		name := ""
		if o.cfg != nil {
			name = o.cfg.AgentName
		}
		reply.Content = strings.TrimSpace(stripReplyConventions(name, strings.TrimSuffix(stopped.sent, s.Unread)))
	case strings.HasSuffix(s.Unread, stopped.sent):
		reply.Content = ""
	}
	if strings.TrimSpace(reply.Content) == "" {
		o.canonical = o.canonical[:last]
		o.working = o.working[:len(o.working)-1]
		o.history = o.history[:len(o.history)-1]
		return
	}
	// A reply recorded as complete was stopped as it ended.
	if reply.Disposition == "" {
		reply.Disposition = "interrupted"
		if s.Reply != nil && s.Reply.Disposition != "" {
			reply.Disposition = s.Reply.Disposition
		}
	}
	if reply.Content == recorded.Content && reply.Disposition == recorded.Disposition {
		return
	}
	o.canonical[last] = reply
	held := &o.working[len(o.working)-1]
	held.Content, held.Disposition = reply.Content, reply.Disposition
	o.history[len(o.history)-1] = o.projectTurn(reply, o.canonical, last, false)
}

var (
	_ StoppedRequestRecorder = (*OllamaBrain)(nil)
	_ StoppedRequestRecorder = (*CodexBrain)(nil)
	_ StoppedRequestRecorder = (*MuseSparkBrain)(nil)
	_ StoppedRequestRecorder = (*ObeyBrain)(nil)
	_ RequestAbandoner       = (*CodexBrain)(nil)
	_ RequestAbandoner       = (*MuseSparkBrain)(nil)
	_ RequestAbandoner       = (*ObeyBrain)(nil)
)
