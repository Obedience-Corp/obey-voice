package work

// Replay re-admits a communicated result for another explicit delivery
// attempt. It only moves the retained immutable outcome back into the local
// pending set; it never invokes the worker or provider again.
func (c *Coordinator) Replay(scope WorkScope, resultID string) error {
	c.mu.Lock()
	if err := c.checkScopeLocked(scope); err != nil {
		c.mu.Unlock()
		return err
	}
	if resultID == "" {
		c.mu.Unlock()
		return ErrUnknown
	}
	if _, exists := c.results[resultID]; exists {
		c.mu.Unlock()
		return ErrConflict
	}
	v, exists := c.communicated[resultID]
	if !exists || v.Outcome.ResultID != resultID || !v.Outcome.Scope.Valid() || v.Outcome.Scope.SessionID != scope.SessionID || v.DeliveryKey.Scope != scope {
		c.mu.Unlock()
		return ErrUnknown
	}
	if c.reserved >= c.pendingLimit {
		c.mu.Unlock()
		return ErrOverflow
	}
	jobID := v.Outcome.JobID
	if jobID == "" {
		c.mu.Unlock()
		return ErrInvalid
	}
	if existing := c.jobs[jobID]; existing != nil {
		c.mu.Unlock()
		return ErrConflict
	}

	v = cloneResult(v)
	v.State = Ready
	v.AnnouncementState = AnnouncementPending
	v.HoldReason = ""
	v.LastAttempt = nil
	v.DeliveryKey = JobKey{Scope: scope, JobID: jobID}
	v.ReceivedRevision = c.revision + 1
	c.results[resultID] = &result{view: v}
	c.jobs[jobID] = &job{view: JobView{
		Key:              v.DeliveryKey,
		Task:             v.Task,
		Origin:           v.Outcome.Origin,
		Provider:         v.Outcome.Provider,
		Model:            v.Outcome.Model,
		Config:           cloneMap(v.Outcome.Config),
		State:            JobTerminal,
		TerminalRevision: v.Outcome.TerminalRevision,
		Outcome:          ptrOutcome(v.Outcome),
	}}
	delete(c.communicated, resultID)
	for i, id := range c.communicatedOrder {
		if id == resultID {
			c.communicatedOrder = append(c.communicatedOrder[:i], c.communicatedOrder[i+1:]...)
			break
		}
	}
	delete(c.tombstones, jobID)
	for i, id := range c.tombstoneOrder {
		if id == jobID {
			c.tombstoneOrder = append(c.tombstoneOrder[:i], c.tombstoneOrder[i+1:]...)
			break
		}
	}
	c.reserved++
	c.revision++
	s, cb := c.snapshotLocked(), c.onChange
	c.mu.Unlock()
	notify(cb, s)
	notifyReady(c)
	return nil
}
