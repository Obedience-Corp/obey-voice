package brain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestCodexToolActivityUsesObservedFacts(t *testing.T) {
	for _, tc := range []struct {
		raw, summary string
		kind         ToolKind
		status       events.ToolOutcomeStatus
	}{
		{`{"type":"commandExecution","command":"go test ./... token=private","status":"completed","exitCode":1,"aggregatedOutput":"secret=private\nfailed","durationMs":42}`, "go test ./...", KindExecute, events.ToolOutcomeFailed},
		{`{"type":"fileChange","changes":[{"path":"app.go"},{"path":"app_test.go"}],"status":"declined"}`, "app_test.go", KindWrite, events.ToolOutcomeFailed},
		{`{"type":"webSearch","query":"weather forecast"}`, "weather forecast", KindWebSearch, events.ToolOutcomeUnknown},
		{`{"type":"commandExecution","command":"pwd","status":"completed","exitCode":0}`, "pwd", KindExecute, events.ToolOutcomeSucceeded},
	} {
		var n codexToolNotice
		if err := json.Unmarshal([]byte(tc.raw), &n); err != nil {
			t.Fatal(err)
		}
		call, result := n.call(), n.result(time.Second)
		if call.Kind != tc.kind || !strings.Contains(call.Summary, tc.summary) || strings.Contains(call.Summary, "private") {
			t.Errorf("call = %+v", call)
		}
		if result.Outcome.Status != tc.status || strings.Contains(result.Preview, "private") {
			t.Errorf("outcome = %+v", result.Outcome)
		}
		if n.DurationMs != nil && result.Elapsed != 42*time.Millisecond {
			t.Errorf("elapsed = %s", result.Elapsed)
		}
	}
}
