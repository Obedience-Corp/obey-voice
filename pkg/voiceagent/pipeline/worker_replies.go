package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"

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
	// admission is the frozen worker config, which names the persona and
	// backend that started the job.
	admission map[string]string
	// continued marks a job that continues a reply moved off the live turn.
	continued bool
	// incomplete is why such a reply ended before it was finished; its
	// result is the part that arrived. keptResponseID names the part given
	// before the move, which is then recorded as unfinished too.
	incomplete     error
	keptResponseID string
	// unspoken marks a result shown and saved but not spoken, as a live reply
	// stops speaking at its output limit.
	unspoken bool
}
type workerReply struct {
	Attachments []chatmedia.Ref
	workerOrigin
	Text string
	// saved marks a held reply already offered to history when it was shown.
	saved bool
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
		a.deliverResult(item, false)
	}
}

// Worker callbacks publish their own reply without borrowing the foreground
// response's streaming buffers. History mutations belong to the serialized
// host, at admission or an idle background turn.
func (a *Agency) queueWorkerReply(id, text string, refs []chatmedia.Ref, held, shown bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reply := workerReply{workerOrigin: a.workerOrigins[id], Text: text, Attachments: refs}
	if shown && strings.TrimSpace(text) != "" {
		a.workerReplies = append(a.workerReplies, reply)
	}
	if held {
		if a.heldReplies == nil {
			a.heldReplies = make(map[string]workerReply)
		}
		reply.saved = shown
		a.heldReplies[id] = reply
	}
}

func (p *Pipeline) hasWorkerReplies() bool {
	if p == nil || p.Agency == nil {
		return false
	}
	p.Agency.mu.Lock()
	defer p.Agency.mu.Unlock()
	return len(p.Agency.workerReplies) > 0
}

func (p *Pipeline) heldResultWaiting() bool {
	return p != nil && p.Agency != nil && p.Agency.heldResultWaiting()
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
		var writeErr error
		writer, supportsMedia := p.Brain.(brain.ReplyMediaWriter)
		if len(reply.Attachments) > 0 && !supportsMedia {
			writeErr = fmt.Errorf("provider cannot persist worker image replies")
		} else if reply.incomplete != nil {
			disposition := brain.IncompleteDisposition(reply.incomplete)
			// The part kept before a hand-off finished; only the worker was forced.
			if brain.ForcedAnswerReason(reply.incomplete) == "" {
				writeErr = markReplyUnfinished(p.Brain, reply.keptResponseID, disposition)
			}
			if writeErr == nil && strings.TrimSpace(reply.Text) != "" {
				writeErr = p.appendIncompleteReply(p.Brain, reply.Text, identity, disposition)
			}
		} else {
			writeErr = p.appendReply(p.Brain, reply.Text, identity, reply.Attachments)
		}
		if err := writeErr; err != nil {
			a.mu.Lock()
			a.workerReplies = append(replies[i:], a.workerReplies...)
			a.mu.Unlock()
			p.emit(events.Error{Stage: "worker result", Message: err.Error()})
			return err
		}
		committed = true
		if len(reply.Attachments) > 0 {
			if err := writer.SetReplyMedia(reply.Text, reply.Attachments); err != nil {
				// The text was appended. Requeue only untouched replies to avoid duplicates.
				a.mu.Lock()
				a.workerReplies = append(replies[i+1:], a.workerReplies...)
				a.mu.Unlock()
				p.emit(events.Error{Stage: "worker result images", Message: err.Error()})
				return err
			}
		}
	}
	return nil
}

// appendIncompleteReply records a reply that ended before it was finished
// with its disposition, as a stopped live reply is recorded, where the
// provider's history can carry one.
func (p *Pipeline) appendIncompleteReply(provider brain.Provider, text string, identity *brain.TurnIdentity, disposition string) error {
	turn := assistantTurn(text, identity, time.Now().UTC())
	turn.Disposition = disposition
	if continuity, ok := provider.(brain.ContinuityTransferProvider); ok {
		return appendContinuityHistory(continuity, []brain.Turn{turn})
	}
	if t := p.transcriptFor(provider); t != nil {
		if err := appendResponse(provider, text, identity); err != nil {
			return err
		}
		return p.appendTranscript(t, turn)
	}
	return p.appendReply(provider, text, identity, nil)
}

// markReplyUnfinished gives the assistant turns of response id, recorded as
// finished, the disposition of a reply that did not finish. Only a provider
// that hands over its whole conversation can be changed this way.
func markReplyUnfinished(provider brain.Provider, id, disposition string) error {
	continuity, ok := provider.(brain.ContinuityTransferProvider)
	if id == "" || !ok {
		return nil
	}
	snapshot := continuity.ContinuitySnapshot()
	changed := false
	mark := func(turns []brain.Turn) {
		for i := range turns {
			if turns[i].Role == "assistant" && turns[i].ResponseID == id && turns[i].Disposition == "" {
				turns[i].Disposition, changed = disposition, true
			}
		}
	}
	mark(snapshot.Canonical)
	mark(snapshot.Working)
	if !changed {
		return nil
	}
	return continuity.ImportContinuity(snapshot)
}

// queueUnfinished records the rest of a reply that will not finish: what
// arrived after the part kept before the move, which may be nothing.
func (a *Agency) queueUnfinished(id, text string, reason error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	origin := a.workerOrigins[id]
	origin.incomplete = reason
	a.workerOrigins[id] = origin
	a.workerReplies = append(a.workerReplies, workerReply{workerOrigin: origin, Text: text})
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

// saveWorkerThreadAt saves a checkpoint unless a newer one is already saved,
// so a late snapshot cannot replace newer evidence.
func (a *Agency) saveWorkerThreadAt(id string, turns []brain.Turn, revision uint64) {
	if id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if revision < a.workerThreadRevs[id] {
		return
	}
	if a.workerThreadRevs == nil {
		a.workerThreadRevs = make(map[string]uint64)
	}
	if a.workerThreads == nil {
		a.workerThreads = make(map[string][]brain.Turn)
	}
	a.workerThreadRevs[id] = revision
	a.workerThreads[id] = copyWorkerThread(turns)
}
func copyWorkerThread(turns []brain.Turn) []brain.Turn {
	copy := append([]brain.Turn(nil), turns...)
	for i := range copy {
		copy[i].Attachments = append([]chatmedia.Ref(nil), turns[i].Attachments...)
		copy[i].OpenedImages = append([]chatmedia.Ref(nil), turns[i].OpenedImages...)
		copy[i].PostedImages = append([]brain.PostedImage(nil), turns[i].PostedImages...)
		copy[i].ToolCalls = append([]brain.TurnToolCall(nil), copy[i].ToolCalls...)
	}
	return copy
}
