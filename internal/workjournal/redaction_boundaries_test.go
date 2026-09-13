package workjournal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestDiagnosticRedactionNestedBoundaries(t *testing.T) {
	secret := "configured-private-token"
	n := NewNormalizer(&config.Config{TTSWorkerToken: secret})
	for _, s := range []string{
		"Authorization: Basic Zm9vOmJhcg==",
		`{"api_key": "private-value"}`,
		"https://someone:private-value@example.test/path?access_token=private-value",
		"-----BEGIN PRIVATE KEY-----\nprivate-value\n-----END PRIVATE KEY-----",
		"opaque " + secret,
	} {
		v := n.Text(s, 16384)
		for _, bad := range []string{secret, "private-value", "Zm9vOmJhcg==", "someone:"} {
			if strings.Contains(v, bad) {
				t.Fatalf("secret survived: %q", v)
			}
		}
	}
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	outcome := work.Outcome{Text: secret, SourceRef: secret, Limitations: secret, Termination: secret, Origin: secret, Config: map[string]string{"worker_plan": secret}}
	s := work.Snapshot{Scope: scope, Jobs: []work.JobView{{State: work.JobRunning, Key: work.JobKey{Scope: scope, JobID: "j"}, Outcome: &outcome}}, Pending: []work.ResultView{{Outcome: outcome}}, Communicated: []work.ResultView{{Outcome: outcome}}}
	copy := n.Snapshot(s)
	b, _ := json.Marshal(copy)
	if strings.Contains(string(b), secret) {
		t.Fatalf("nested secret survived: %s", b)
	}
	if s.Pending[0].Outcome.Text != secret || s.Jobs[0].Outcome.Termination != secret {
		t.Fatal("canonical input modified")
	}
	r, ok := n.Normalize(events.ToolCallFinished{JobID: "j", ID: "call", WorkScope: scope, Preview: "Authorization: Basic Zm9vOmJhcg=="}, s)
	if !ok || r.DetailPayload == nil || strings.Contains(r.DetailPayload.Text, "Zm9vOmJhcg==") {
		t.Fatalf("unsafe details: %+v", r.DetailPayload)
	}
}

func TestDiagnosticProjectionKeepsEveryActiveAndPendingJob(t *testing.T) {
	s := work.Snapshot{}
	for range 110 {
		s.Jobs = append(s.Jobs, work.JobView{State: work.JobTerminal})
	}
	s.Jobs = append(s.Jobs, work.JobView{State: work.JobRunning, Task: "active"})
	s.Pending = []work.ResultView{{State: work.Held, Task: "unseen"}}
	got := NewNormalizer(nil).Snapshot(s)
	if len(got.Jobs) != 101 || len(got.Pending) != 1 {
		t.Fatalf("projection discarded active/pending: %+v", got)
	}
}
