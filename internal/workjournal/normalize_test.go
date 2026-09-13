package workjournal

import (
	"encoding/json"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestNormalizeRejectsUnknownJobAndCopiesSnapshot(t *testing.T) {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	snap := work.Snapshot{Scope: scope, Jobs: []work.JobView{{Key: work.JobKey{Scope: scope, JobID: "job"}, Task: "safe", Config: map[string]string{"secret": "token=x"}}}}
	n := NewNormalizer(&config.Config{ActivePersona: "p", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "m"})
	if _, ok := n.Normalize(events.ToolCallStarted{JobID: "unknown", Name: "read_file"}, snap); ok {
		t.Fatal("unknown job normalized")
	}
	r, ok := n.Normalize(events.ToolCallStarted{JobID: "job", ID: "call", Name: "read_file", Summary: "token=bad"}, snap)
	if !ok {
		t.Fatal("known job rejected")
	}
	var data map[string]any
	_ = json.Unmarshal(r.Data, &data)
	if data["summary"] == "token=bad" {
		t.Fatal("tool secret was not redacted")
	}
	data["summary"] = "mutated"
	if snap.Jobs[0].Config["secret"] != "token=x" {
		t.Fatal("snapshot input mutated")
	}
}

func TestNormalizeConfigAllowlistExcludesSecrets(t *testing.T) {
	n := NewNormalizer(&config.Config{OllamaHost: "http://user:pass@example", TTSWorkerToken: "secret", ActivePersona: "p", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "m"})
	c := n.Config()
	if _, ok := c["endpoint"]; ok {
		t.Fatal("endpoint exported")
	}
	if _, ok := c["token"]; ok {
		t.Fatal("token exported")
	}
	if c["legacy_worker_model"] != "m" {
		t.Fatalf("model=%v", c["legacy_worker_model"])
	}
}

func TestSnapshotDeepCopiesNestedEvidenceAndBoundsJobs(t *testing.T) {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	snap := work.Snapshot{Scope: scope}
	for i := 0; i < 101; i++ {
		snap.Jobs = append(snap.Jobs, work.JobView{Key: work.JobKey{Scope: scope, JobID: "job"}, State: work.JobTerminal, TerminalRevision: uint64(i + 1), Task: "task"})
	}
	snap.Pending = []work.ResultView{{Task: "pending", Outcome: work.Outcome{Text: "token=secret", Config: map[string]string{"worker_plan": "token=secret"}}, LastAttempt: &work.DeliveryEvidence{Reason: "api_key=secret", Receipt: work.DeliveryReceipt{Mode: "token=secret"}}}}
	n := NewNormalizer(&config.Config{TTSWorkerToken: "secret"})
	safe := n.Snapshot(snap)
	if len(safe.Jobs) != 100 || len(safe.Pending) != 1 {
		t.Fatalf("snapshot sizes jobs=%d pending=%d", len(safe.Jobs), len(safe.Pending))
	}
	if safe.Pending[0].LastAttempt == snap.Pending[0].LastAttempt {
		t.Fatal("last attempt was aliased")
	}
	if safe.Pending[0].Outcome.Config == nil || len(safe.Pending[0].Outcome.Config) != 0 {
		t.Fatal("unsafe outcome config exported")
	}
	safe.Pending[0].LastAttempt.Reason = "mutated"
	if snap.Pending[0].LastAttempt.Reason == "mutated" {
		t.Fatal("mutating normalized evidence changed source")
	}
	var data map[string]any
	r, ok := n.Normalize(events.WorkStateChanged{Snapshot: snap}, snap)
	if !ok || json.Unmarshal(r.Data, &data) != nil {
		t.Fatal("work snapshot did not normalize")
	}
	if data["omitted_jobs"] != float64(1) {
		t.Fatalf("omitted_jobs=%v", data["omitted_jobs"])
	}
}
