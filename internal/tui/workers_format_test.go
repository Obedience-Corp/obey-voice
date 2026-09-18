package tui

import (
	"strings"
	"testing"
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
