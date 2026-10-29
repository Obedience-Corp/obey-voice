package pipeline

import (
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type workerOrigin struct {
	Trace         events.TraceContext
	traceQueuedAt time.Time
	Scope         work.WorkScope
	Envelope      events.ResponseEnvelope
	Voices        responseVoices
	releaseVoice  func()
}
type workerReply struct {
	workerOrigin
	Text string
}

func (a *Agency) discardPendingSpeech() {
	a.mu.Lock()
	pending := a.pending
	a.pending = nil
	a.mu.Unlock()
	for _, item := range pending {
		if item.releaseVoice != nil {
			item.releaseVoice()
		}
	}
}

// Worker callbacks publish their own reply without borrowing the foreground
// response's streaming buffers. History mutations belong to the serialized
// host, at admission or an idle background turn.
func (a *Agency) queueWorkerReply(id, text string) workerOrigin {
	a.mu.Lock()
	defer a.mu.Unlock()
	origin := a.workerOrigins[id]
	if strings.TrimSpace(text) != "" {
		a.workerReplies = append(a.workerReplies, workerReply{workerOrigin: origin, Text: text})
	}
	return origin
}

func (p *Pipeline) hasWorkerReplies() bool {
	if p == nil || p.Agency == nil {
		return false
	}
	p.Agency.mu.Lock()
	defer p.Agency.mu.Unlock()
	return len(p.Agency.workerReplies) > 0
}

func (p *Pipeline) commitWorkerReplies() error {
	if !p.hasWorkerReplies() || p.Brain == nil {
		return nil
	}
	a := p.Agency
	a.mu.Lock()
	replies := a.workerReplies
	a.workerReplies = nil
	a.mu.Unlock()
	committed := false
	defer func() {
		if committed && p.OnTurn != nil {
			p.OnTurn()
		}
	}()
	for i, reply := range replies {
		if reply.Scope.Valid() && (reply.Scope != p.ConversationScope() || p.ConversationRetired()) {
			continue
		}
		var identity *brain.TurnIdentity
		if e := reply.Envelope; e.PersonaID != "" {
			identity = &brain.TurnIdentity{PersonaID: e.PersonaID, PersonaDisplayName: e.DisplayName, PromptHash: e.PromptHash, ResponseID: e.ResponseID}
		}
		if err := appendResponse(p.Brain, reply.Text, identity); err != nil {
			a.mu.Lock()
			a.workerReplies = append(replies[i:], a.workerReplies...)
			a.mu.Unlock()
			p.emit(events.Error{Stage: "worker result", Message: err.Error()})
			return err
		}
		committed = true
	}
	return nil
}

// Capture only on the worker's own call stack. Reading a provider's history
// concurrently from the UI would race its tool loop.
func (a *Agency) captureWorkerThread(id string, worker brain.Provider) {
	if turns := worker.History(); len(turns) > 0 {
		a.saveWorkerThread(id, turns)
	}
}
func (a *Agency) saveWorkerThread(id string, turns []brain.Turn) {
	if id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workerThreads == nil {
		a.workerThreads = make(map[string][]brain.Turn)
	}
	a.workerThreads[id] = copyWorkerThread(turns)
}
func copyWorkerThread(turns []brain.Turn) []brain.Turn {
	copy := append([]brain.Turn(nil), turns...)
	for i := range copy {
		copy[i].ToolCalls = append([]brain.TurnToolCall(nil), copy[i].ToolCalls...)
	}
	return copy
}
