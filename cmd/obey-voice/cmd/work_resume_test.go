//go:build !integration

package cmd

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestServeWorkResumeSeparatesSavedConversations(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	p, cfg := checkpointPipeline(t)
	ref := &sessionRef{sess: session.New("ollama", "test")}
	if err := ref.bindWork(p, cfg, ref.sess); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.closeWork(p) }()
	p.Brain.LoadHistory([]brain.Turn{{Role: "user", Content: "Previous conversation"}})
	oldID := ref.id()
	oldResult := publishCheckpointResult(t, p, "Previous result")
	oldCheckpoint := ref.checkpoint
	oldSnapshot := p.WorkSnapshot()

	next := session.New("ollama", "test")
	if err := next.Save([]brain.Turn{{Role: "user", Content: "Selected conversation"}}); err != nil {
		t.Fatal(err)
	}
	source := work.WorkScope{SessionID: next.ID, RuntimeEpoch: "saved-runtime", ConversationEpoch: "saved-conversation"}
	c := work.NewCoordinator(source)
	key, err := c.Register(source, work.Admission{Task: "Saved check", Origin: "managed", Provider: "obey"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(key, work.Outcome{Scope: source, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "Selected result"}); err != nil {
		t.Fatal(err)
	}
	if err := session.DefaultStore().BeginWork(next, c.Snapshot(source), nil); err != nil {
		t.Fatal(err)
	}

	if err := ref.resume(p, cfg, next.ID); err != nil {
		t.Fatal(err)
	}
	oldCheckpoint.submit(oldSnapshot)
	got := p.WorkSnapshot()
	if ref.id() != next.ID || got.Scope.SessionID != next.ID || got.Scope.RuntimeEpoch == source.RuntimeEpoch {
		t.Fatalf("resume scope = %+v", got.Scope)
	}
	if len(got.Pending) != 1 || got.Pending[0].Outcome.Text != "Selected result" || got.Pending[0].State != work.Held {
		t.Fatalf("resume pending = %+v", got.Pending)
	}
	if history := p.Brain.History(); len(history) != 1 || history[0].Content != "Selected conversation" {
		t.Fatalf("resume history = %+v", history)
	}
	previous, err := session.Load(oldID)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Work == nil || !previous.Work.Retired || len(previous.Work.Pending) != 1 || previous.Work.Pending[0].Outcome.ResultID != oldResult.Outcome.ResultID {
		t.Fatalf("previous checkpoint = %+v", previous.Work)
	}
	if len(previous.Turns) != 1 || previous.Turns[0].Content != "Previous conversation" {
		t.Fatalf("previous history = %+v", previous.Turns)
	}
	if err := ref.closeWork(p); err != nil {
		t.Fatal(err)
	}
	selected, err := session.Load(next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Work.Pending) != 1 || selected.Work.Pending[0].Outcome.Text != "Selected result" {
		t.Fatalf("late old save crossed sessions: %+v", selected.Work)
	}
}

func TestServeWorkResumeInvalidTargetRestoresPreviousSelection(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	p, cfg := checkpointPipeline(t)
	ref := &sessionRef{sess: session.New("ollama", "test")}
	if err := ref.bindWork(p, cfg, ref.sess); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.closeWork(p) }()
	previousID := ref.id()
	p.Brain.LoadHistory([]brain.Turn{{Role: "user", Content: "Keep this conversation"}})
	publishCheckpointResult(t, p, "Keep this result")
	invalid := session.New("ollama", "test")
	invalid.Work = &work.Snapshot{Scope: work.WorkScope{SessionID: invalid.ID}} // no runtime/conversation fencing
	if err := invalid.Save([]brain.Turn{{Role: "user", Content: "Invalid target"}}); err != nil {
		t.Fatal(err)
	}
	if err := ref.resume(p, cfg, invalid.ID); err == nil {
		t.Fatal("invalid work restore accepted")
	}
	if ref.id() != previousID {
		t.Fatal("failed restore changed selected session")
	}
	got := p.WorkSnapshot()
	if got.Retired || got.Scope.SessionID != previousID || len(got.Pending) != 1 || got.Pending[0].State != work.Held || got.Pending[0].Outcome.Text != "Keep this result" {
		t.Fatalf("rollback state = %+v", got)
	}
	if history := p.Brain.History(); len(history) != 1 || history[0].Content != "Keep this conversation" {
		t.Fatalf("rollback history = %+v", history)
	}
}
