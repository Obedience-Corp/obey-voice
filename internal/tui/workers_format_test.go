package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestSplitJSONPayloadFindsEmbeddedObject(t *testing.T) {
	prefix, pretty, ok := splitJSONPayload(`searched 1 time (1 unknown) · {"query":"Denver CO","ok":true}`)
	if !ok {
		t.Fatal("did not find JSON")
	}
	if prefix != "searched 1 time (1 unknown)" {
		t.Fatalf("prefix = %q", prefix)
	}
	if !strings.Contains(pretty, "\n") || !strings.Contains(pretty, `"query"`) {
		t.Fatalf("pretty = %q", pretty)
	}
}

func TestColorJSONEmitsDistinctSpans(t *testing.T) {
	colored := colorJSON("{\n  \"query\": \"Denver\",\n  \"temp\": 48,\n  \"ok\": true\n}")
	if !strings.Contains(colored, "\x1b[") {
		t.Fatal("expected SGR")
	}
	plain := stripANSI(colored)
	if !strings.Contains(plain, `"query"`) || !strings.Contains(plain, "48") {
		t.Fatalf("stripped = %q", plain)
	}
}

func TestWorkerActivitySummaryDropsJSON(t *testing.T) {
	got := workerActivitySummary(`searched 1 time · {"url":"https://example.com/very/long"}`)
	if strings.Contains(got, "example.com") {
		t.Fatalf("summary kept URL: %q", got)
	}
	if got != "searched 1 time" {
		t.Fatalf("summary = %q", got)
	}
}

func TestWorkerActivitySummaryDropsTruncatedPreview(t *testing.T) {
	activity := `searched 1 time (1 unknown) · {"query":"Denver CO current temperature today","results":[{"title":"AccuWeather"`
	got := workerActivitySummary(activity)
	if got != "searched 1 time (1 unknown)" {
		t.Fatalf("summary = %q", got)
	}
	m := newWorkers(&config.Config{}, &testWorkers{items: []pipeline.WorkerInfo{{
		ID: "job-1", Task: "weather", State: "running", Activity: activity,
	}}})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if list := stripANSI(m.View()); strings.Contains(list, "AccuWeather") {
		t.Fatalf("list dumped truncated JSON:\n%s", list)
	}
}

func TestIndentJSONPreservesKeyOrderAndIntegerLiteral(t *testing.T) {
	pretty, ok := indentJSON(`{"b":1,"a":9007199254740993}`)
	if !ok {
		t.Fatal("indent failed")
	}
	if !strings.Contains(pretty, `"b": 1`) || !strings.Contains(pretty, `"a": 9007199254740993`) {
		t.Fatalf("rewrote numbers or keys: %q", pretty)
	}
	if strings.Index(pretty, `"b"`) > strings.Index(pretty, `"a"`) {
		t.Fatalf("keys sorted: %q", pretty)
	}
}

func TestColorJSONKeysAfterBraceInString(t *testing.T) {
	colored := colorJSON("{\n  \"a\": \"}\",\n  \"b\": 1\n}")
	if !strings.Contains(colored, jsonKeyStyle.Render(`"b"`)) {
		t.Fatalf("b should be a key:\n%s", colored)
	}
}

func TestWrapWorkerPlainStripsANSI(t *testing.T) {
	got := wrapWorkerPlain("hi\x1b[31mred", 40)
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("SGR leaked: %q", got)
	}
	if !strings.Contains(stripANSI(got), "hired") && !strings.Contains(stripANSI(got), "red") {
		t.Fatalf("text lost: %q", got)
	}
}
