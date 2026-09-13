package pipeline

import (
	"errors"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// WorkResultAction changes canonical disposition only. The host's existing
// background turn gate handles speech after capture and foreground work yield.
func (p *Pipeline) WorkResultAction(scope work.WorkScope, id, action string) error {
	c := p.WorkCoordinator()
	if c == nil {
		return errors.New("result controls require conversation work")
	}
	snap := c.Snapshot(scope)
	if c.Scope() != scope || snap.Retired {
		return work.ErrStaleScope
	}
	var selected *work.ResultView
	for _, rows := range [][]work.ResultView{snap.Pending, snap.Communicated} {
		for _, r := range rows {
			if r.Outcome.JobID == id || r.Outcome.ResultID == id {
				copy := r
				selected = &copy
				break
			}
		}
	}
	if selected == nil {
		return fmt.Errorf("%w: no retained result for %s", work.ErrUnknown, id)
	}
	result := selected.Outcome.ResultID
	var err error
	switch action {
	case "dismiss":
		if selected.State == work.Communicated || selected.State == work.Dismissed {
			return fmt.Errorf("result %s is already %s; nothing to dismiss", result, selected.State)
		}
		err = c.Dismiss(scope, result)
	case "speak", "replay":
		if p.OutputMuted() {
			return errors.New("audio is off; enable audio before requesting speech")
		}
		if p.Player == nil || !p.ttsReady() {
			return errors.New("audio output is unavailable; the result remains readable")
		}
		if selected.State == work.Communicated {
			err = c.Replay(scope, result)
		} else {
			err = c.Resume(scope, result)
		}
	default:
		return fmt.Errorf("unknown work result action %q", action)
	}
	if err != nil {
		return err
	}
	p.emit(events.WorkDelivery{Scope: scope, ResultIDs: []string{result}, Stage: "user_" + action, Reason: "explicit_user_request"})
	return nil
}
