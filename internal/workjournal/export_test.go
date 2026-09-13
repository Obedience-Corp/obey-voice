package workjournal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestReportReprojectsMaliciousSavedRecords(t *testing.T) {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	snapshot := work.Snapshot{Scope: scope, Jobs: []work.JobView{{Key: work.JobKey{Scope: scope, JobID: "j"}, Config: map[string]string{"worker_plan": "api_key=BAD", "provider": "ollama"}, Outcome: &work.Outcome{Text: "api_key=BAD", Config: map[string]string{"secret": "BAD"}}}}, Pending: []work.ResultView{{Outcome: work.Outcome{ResultID: "result", Scope: scope, JobID: "j", Text: "Authorization: Basic BAD", SourceRef: "token=BAD", Config: map[string]string{"secret": "BAD"}}, LastAttempt: &work.DeliveryEvidence{Reason: "password=BAD"}}}}
	workPayload := mustJSON(map[string]any{"snapshot": snapshot, "full_detail_body": "hidden reasoning BAD", "omitted_jobs": 0})
	src := Report{Schema: Schema, Records: []Record{{Schema: Schema, SessionID: "s", RuntimeID: "r", ConversationEpoch: "c", Type: "work.snapshot", Data: workPayload}, {Schema: Schema, SessionID: "s", RuntimeID: "r", ConversationEpoch: "c", Type: "tool.finished", Data: mustJSON(map[string]any{"name": "run_command", "preview": "Authorization: Basic BAD", "outcome_detail": map[string]any{"status": "unknown", "exit_code": nil, "http_status": nil, "reason": "hidden reasoning BAD"}, "full_detail_body": "BAD"})}, {Schema: Schema, Type: "future.unknown", SessionID: "s", RuntimeID: "r", Data: mustJSON(map[string]any{"secret": "BAD"})}}}
	before, _ := json.Marshal(src)
	n := NewNormalizer(&config.Config{TTSWorkerToken: "BAD"})
	got := n.Report(src)
	after, _ := json.Marshal(src)
	if string(before) != string(after) {
		t.Fatal("source report mutated")
	}
	b, _ := json.Marshal(got)
	text := string(b)
	if strings.Contains(text, "BAD") {
		t.Fatalf("export leaked adversarial secret: %s", text)
	}
	var payload map[string]any
	_ = json.Unmarshal(got.Records[1].Data, &payload)
	if _, ok := payload["full_detail_body"]; ok {
		t.Fatal("full detail body exported")
	}
	if payload["outcome_detail"].(map[string]any)["exit_code"] != nil {
		t.Fatal("nullable exit code changed")
	}
	var future map[string]any
	_ = json.Unmarshal(got.Records[2].Data, &future)
	if future["unrecognized_payload_omitted"] != true {
		t.Fatal("unknown payload was not marked omitted")
	}
}

func TestReportKeepsKnownProseButDropsUnsafeSnapshotConfig(t *testing.T) {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	snap := work.Snapshot{Scope: scope, Pending: []work.ResultView{{Outcome: work.Outcome{Text: "normal limitations prose", Config: map[string]string{"worker_plan": "secret"}}}}}
	r := Report{Schema: Schema, Checkpoints: []Checkpoint{{Schema: Schema, Snapshot: snap}}, Records: []Record{{Schema: Schema, Type: "worker.usage", Data: mustJSON(map[string]any{"input_tokens": nil, "output_tokens": nil, "charge_micro_usd": nil, "unknown": "secret"})}}}
	g := NewNormalizer(nil).Report(r)
	b, _ := json.Marshal(g)
	if !strings.Contains(string(b), "normal limitations prose") {
		t.Fatal("known prose omitted")
	}
	if strings.Contains(string(b), "worker_plan") {
		t.Fatal("unsafe config key exported")
	}
}
