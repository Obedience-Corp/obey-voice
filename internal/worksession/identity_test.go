package worksession

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestCheckpointSubmissionIdentityDoesNotRaceTranscriptSave(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := session.New("ollama", "fixture")
	if err := store.Save(sess, nil); err != nil {
		t.Fatal(err)
	}
	id := sess.ID
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	p := &pipeline.Pipeline{Events: events.NewBus()}
	cfg := &config.Config{VoiceWorkEnabled: true, VoiceWorkerProvider: "ollama"}
	agency := pipeline.NewAgency(p, jobs, nil, cfg, nil)
	agency.Wire()
	checkpoint, err := Bind(p, cfg, sess, store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := checkpoint.Close(); err != nil {
			t.Error(err)
		}
	}()
	snapshot := p.WorkSnapshot()
	started, done := make(chan struct{}), make(chan struct{})
	// Park publication at its queue lock while the independently serialized
	// transcript save replaces the caller Session. Identity admission occurs
	// before that queue lock and must use the constructor's immutable copy.
	checkpoint.mu.Lock()
	go func() { defer close(done); close(started); checkpoint.Submit(snapshot) }()
	<-started
	err = store.Save(sess, []brain.Turn{{Role: "user", Content: "A real transcript save"}})
	checkpoint.mu.Unlock()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sess.ID != id {
		t.Fatal("session identity changed during normal Save")
	}
	loaded, err := store.Load(id)
	if err != nil || len(loaded.Turns) != 1 {
		t.Fatal("transcript save lost", err)
	}
}
