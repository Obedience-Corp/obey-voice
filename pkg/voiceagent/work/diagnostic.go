package work

import "time"

// DiagnosticStamp orders diagnostic observations within one runtime. It does
// not grant execution, delivery, or persistence authority.
type DiagnosticStamp struct {
	Scope    WorkScope
	Seq      uint64
	Revision uint64
	At       time.Time
	MonoMS   int64
}

// RecordDiagnostic serializes sequence allocation and bounded memory enqueue.
// enqueue must not perform I/O or call RecordDiagnostic recursively. The state
// mutex is released before enqueue; bus consumers run after this call returns.
// Conversation reset keeps the runtime sequence, and stale scopes are fenced.
func (c *Coordinator) RecordDiagnostic(scope WorkScope, enqueue func(DiagnosticStamp)) bool {
	if c == nil || enqueue == nil {
		return false
	}
	c.diagnosticMu.Lock()
	defer c.diagnosticMu.Unlock()
	c.mu.Lock()
	if !scope.Valid() || c.scope != scope {
		c.mu.Unlock()
		return false
	}
	revision := c.revision
	c.mu.Unlock()
	now := time.Now()
	if c.diagnosticStart.IsZero() {
		c.diagnosticStart = now
	}
	c.diagnosticSeq++
	enqueue(DiagnosticStamp{Scope: scope, Seq: c.diagnosticSeq, Revision: revision, At: now.UTC(), MonoMS: now.Sub(c.diagnosticStart).Milliseconds()})
	return true
}
