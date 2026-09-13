package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestWriterPersistsUnknownWorkerUsageAsNullAndLimitReached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-policy.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	w.Attach(bus)
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	bus.Emit(events.WorkerUsageRecorded{Scope: scope, JobID: "job-1", Requests: nil, ToolCalls: nil, Retries: nil})
	bus.Emit(events.WorkerLimitReached{Scope: scope, JobID: "job-1", Message: "model request limit reached", Termination: "unknown"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var usage, limit map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		switch line["type"] {
		case "worker.usage":
			usage, _ = line["worker_usage"].(map[string]any)
		case "worker.limit_reached":
			limit, _ = line["worker_limit"].(map[string]any)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if usage == nil {
		t.Fatal("worker.usage record missing")
	}
	for _, key := range []string{"requests", "tool_calls", "retries", "input_tokens", "output_tokens", "charge_micro_usd"} {
		value, ok := usage[key]
		if !ok || value != nil {
			t.Errorf("worker.usage.%s = %#v (present=%v), want explicit null", key, value, ok)
		}
	}
	if limit == nil || limit["message"] != "model request limit reached" || limit["termination"] != "unknown" {
		t.Fatalf("worker.limit_reached = %#v", limit)
	}
}

func TestWriterPersistsWorkerChargeStatesWithRequestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "charges.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	w.Attach(bus)
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	for _, state := range []string{"checking", "dispatched", "uncertain"} {
		bus.Emit(events.WorkerChargeState{Scope: scope, JobID: "job", RequestID: "req-42", State: state, ChargeCapAvailable: false})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var states []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if line["type"] != "worker.charge" {
			continue
		}
		c, ok := line["worker_charge"].(map[string]any)
		if !ok {
			t.Fatal("worker_charge missing")
		}
		if c["request_id"] != "req-42" {
			t.Fatalf("request id=%v", c["request_id"])
		}
		states = append(states, c["state"].(string))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(states) != 3 || states[0] != "checking" || states[1] != "dispatched" || states[2] != "uncertain" {
		t.Fatalf("states=%v", states)
	}
}
