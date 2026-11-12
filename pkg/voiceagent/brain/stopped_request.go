package brain

import (
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

// replaceStoppedRequest takes the turns a request recorded, canonical[start:]
// and the same number from the end of working, and puts the stopped request
// in their place.
func replaceStoppedRequest(canonical, working []Turn, start int, s StoppedRequest) ([]Turn, []Turn) {
	recorded := len(canonical) - min(max(start, 0), len(canonical))
	canonical = canonical[:len(canonical)-recorded]
	working = working[:len(working)-min(recorded, len(working))]
	for _, t := range []*Turn{s.Line, s.Reply} {
		if t != nil {
			canonical = append(canonical, deepCopyTurns([]Turn{*t})...)
			working = append(working, deepCopyTurns([]Turn{*t})...)
		}
	}
	return canonical, working
}

func (c *CodexBrain) RecordStoppedRequest(s StoppedRequest) {
	c.canonical, c.working = replaceStoppedRequest(c.canonical, c.working, c.requestStart, s)
	// The thread may hold more of the reply than the turn read.
	c.dropThread()
	c.trimWorking()
}

func (b *MuseSparkBrain) RecordStoppedRequest(s StoppedRequest) {
	b.canonical, b.working = replaceStoppedRequest(b.canonical, b.working, b.requestStart, s)
	b.trimWorking()
}

func (b *ObeyBrain) RecordStoppedRequest(s StoppedRequest) {
	b.canonical, b.working = replaceStoppedRequest(b.canonical, b.working, b.requestStart, s)
	b.trimWorking()
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
)
