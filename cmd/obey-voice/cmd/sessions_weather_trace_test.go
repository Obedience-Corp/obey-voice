package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/workjournal"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// Exercise coordinator -> observer -> durable journal -> public commands. Tool
// and audio events are controlled host observations, not a live weather request
// or evidence that physical speech was heard.
func TestSessionsWeatherTraceFromRuntimeJournal(t *testing.T) {
	root := sessionsEnv(t)
	id := "weather-runtime"
	scope := work.WorkScope{SessionID: id, RuntimeEpoch: "weather-run", ConversationEpoch: "chat"}
	bus := events.NewBus()
	c := work.NewCoordinator(scope, work.WithChangeCallback(func(s work.Snapshot) { bus.Emit(events.WorkStateChanged{Snapshot: s}) }))
	j := workjournal.New(filepath.Join(root, id+".work"), c, &config.Config{}, workjournal.Options{}, nil)
	detach := bus.ObserveBeforeFanout(j.Observe)
	defer detach()
	defer j.Close(context.Background())
	key, err := c.Register(scope, work.Admission{JobID: "weather-job", Task: "What is the weather?", Origin: "agency", Config: map[string]string{"persona": "samantha"}})
	if err != nil {
		t.Fatal(err)
	}
	bus.Emit(events.RouteDecided{Scope: scope, JobID: key.JobID, Kind: "spawn", State: "accepted"})
	bus.Emit(events.ToolCallStarted{WorkScope: scope, JobID: key.JobID, ID: "weather-tool", Name: "run_command", Summary: "fixture forecast request"})
	bus.Emit(events.ToolCallFinished{WorkScope: scope, JobID: key.JobID, ID: "weather-tool", Name: "run_command"})
	result, err := c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, ResultID: "forecast-result", Status: work.OutcomeSucceeded, Text: "Fixture forecast: clear", EndedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	bus.Emit(events.WorkDelivery{Scope: scope, Stage: "projected", ResultIDs: []string{result.Outcome.ResultID}, AttemptID: "audio-attempt", Mode: "voice"})
	bus.Emit(events.WorkDelivery{Scope: scope, Stage: "held", ResultIDs: []string{result.Outcome.ResultID}, AttemptID: "audio-attempt", Reason: "muted", Mode: "voice"})
	if err := c.HoldWithReason(scope, result.Outcome.ResultID, "muted"); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot(scope)
	snap.PersistedRevision = snap.Revision
	writeSessionFile(t, root, session.Session{ID: id, FormatVersion: session.CurrentFormatVersion, UpdatedAt: time.Now(), SelectedPersonaID: "santa", SelectedPersonaDisplayName: "Santa", Work: &snap})
	if err := j.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	output, err := runSessionsCmd(t, "events", id, "--job", "weather-job", "--json")
	if err != nil {
		t.Fatal(err, output)
	}
	for _, want := range []string{"work.snapshot", "route.decided", "tool.started", "tool.finished", `"stage":"projected"`, `"stage":"held"`, `"reason":"muted"`, "forecast-result"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %s in trace:\n%s", want, output)
		}
	}
	output, err = runSessionsCmd(t, "inspect", id, "--json")
	if err != nil {
		t.Fatal(err, output)
	}
	var view sessionsWorkView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Work.Pending) != 1 || view.Work.Pending[0].State != work.Held || len(view.Work.Communicated) != 0 || view.SelectedPersonaID != "santa" || view.Work.Pending[0].Outcome.Config["persona"] != "samantha" {
		t.Fatalf("incorrect saved projection: %+v", view.Work)
	}
	before, _ := os.ReadFile(filepath.Join(root, id+".json"))
	export := filepath.Join(t.TempDir(), "trace.json")
	if _, err := runSessionsCmd(t, "export", id, "--output", export); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, id+".json"))
	if string(before) != string(after) {
		t.Fatal("export altered held work")
	}
}
