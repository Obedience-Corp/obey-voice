package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

const fileCompletionAnswer = `I created the file worker-proof.txt with the content "blue comet" in the current working directory.`

type fileCompletionBrain struct {
	*handoffCompletionBrain
	suppressUpdate bool
}

func (b *fileCompletionBrain) Complete(_ context.Context, req brain.CompletionRequest) (brain.CompletionEnvelope, error) {
	text := fileCompletionAnswer + " Let me know if you need anything else."
	if b.suppressUpdate {
		text = "```sh\nprintf hidden-result\n```"
	}
	return brain.CompletionEnvelope{Answer: fileCompletionAnswer, Updates: []brain.CompletionUpdate{{ResultID: req.Results[0].ResultID, Text: text}}}, nil
}

func TestCanonicalFileCompletionMultipleSentences(t *testing.T) {
	for _, suppressed := range []bool{false, true} {
		name := "duplicate already queued"
		if suppressed {
			name = "distinct content suppressed"
		}
		t.Run(name, func(t *testing.T) {
			p, c, scope := newCompletionLeasePipeline(t, false)
			p.Brain = &fileCompletionBrain{handoffCompletionBrain: &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{}}, suppressUpdate: suppressed}
			p.Player = &fencedHandoffPlayer{}
			tts := &fakeTTS{delay: time.Millisecond}
			p.ReplaceTTS(tts, nil)
			if err := c.AcknowledgePersisted(scope, c.Snapshot(scope).Revision); err != nil {
				t.Fatal(err)
			}
			if err := p.RunBackgroundTurn(t.Context()); err != nil {
				t.Fatal(err)
			}
			snapshot := c.Snapshot(scope)
			if suppressed {
				if len(snapshot.Communicated) != 0 || len(snapshot.Pending) != 1 || snapshot.Pending[0].State != work.Held {
					t.Fatalf("unspoken result consumed: %+v", snapshot)
				}
				return
			}
			if len(snapshot.Communicated) != 1 || snapshot.Communicated[0].State != work.Communicated {
				t.Fatalf("played file completion not communicated: %+v; speech=%v", snapshot.Pending, tts.Texts())
			}
			if strings.Count(strings.Join(tts.Texts(), " "), fileCompletionAnswer) != 1 {
				t.Fatalf("repeated answer must be spoken once: %v", tts.Texts())
			}
		})
	}
}
