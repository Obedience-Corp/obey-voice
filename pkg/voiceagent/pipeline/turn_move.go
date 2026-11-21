package pipeline

// MoveTurnToBackground asks the running turn to hand its model request to a
// background job instead of ending it, because input names another persona.
// It reports whether the turn accepted; when it did not, the caller stops the
// turn as before.
func (p *Pipeline) MoveTurnToBackground(input string) bool {
	return false
}
