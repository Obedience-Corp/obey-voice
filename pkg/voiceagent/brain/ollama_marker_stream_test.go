package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestHistoricalAssistantMarkerFilterHandlesBoundariesAndOrdinaryBrackets(t *testing.T) {
	var filter historicalAssistantMarkerFilter
	var got strings.Builder
	for _, chunk := range []string{
		"Sure [Hist",
		"orical assistant: Obey response OLD1] thing [Hist",
		"orical assistant: Obey response OLD2] [not a marker",
	} {
		got.WriteString(filter.Feed(chunk))
	}
	got.WriteString(filter.Flush())

	if want := "Sure  thing  [not a marker"; got.String() != want {
		t.Fatalf("filtered stream = %q, want %q", got.String(), want)
	}

	var incomplete historicalAssistantMarkerFilter
	got.Reset()
	got.WriteString(incomplete.Feed("keep [Historical assistant: unfinished"))
	got.WriteString(incomplete.Flush())
	if got.String() != "keep [Historical assistant: unfinished" {
		t.Fatalf("incomplete marker = %q, want original text", got.String())
	}
}

func TestThinkStreamFiltersHistoricalMarkersButKeepsCanonicalEvidence(t *testing.T) {
	client := markerStreamClient(t, []string{
		`{"model":"m","message":{"role":"assistant","content":"Sure thing. [Hist"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"orical assistant: Obey response OLDRESPONSE1] I am "},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"Obey."},"done":true,"done_reason":"stop"}`,
	})
	o := &OllamaBrain{client: client, model: "m", cfg: &config.Config{MaxHistory: 10, ActivePersona: "samantha", AgentName: "Samantha"}}

	stream, err := o.ThinkStream(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	spoken, _ := drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil {
		t.Fatalf("stream result = %+v", result)
	}
	if strings.Contains(spoken, "Historical assistant") || strings.Contains(spoken, "OLDRESPONSE1") {
		t.Fatalf("spoken output leaked marker: %q", spoken)
	}
	if !strings.Contains(spoken, "Sure thing.") || !strings.Contains(spoken, "I am Obey.") {
		t.Fatalf("spoken output lost reply text: %q", spoken)
	}

	messages := o.buildMessages("")
	last := messages[len(messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Samantha said:") || strings.Contains(last.Content, "OLDRESPONSE1") {
		t.Fatalf("next request gave contaminated output assistant authority: %+v", last)
	}
	canonical := o.CanonicalHistory()
	if len(canonical) == 0 || !strings.Contains(canonical[len(canonical)-1].Content, "OLDRESPONSE1") {
		t.Fatalf("canonical history lost marker evidence: %+v", canonical)
	}
}

func TestThinkFullFiltersHistoricalMarkersButKeepsCanonicalEvidence(t *testing.T) {
	client := markerStreamClient(t, []string{
		`{"model":"m","message":{"role":"assistant","content":"Sure thing. [Historical assistant: Obey response OLDRESPONSE1] I am Obey."},"done":true,"done_reason":"stop"}`,
	})
	o := &OllamaBrain{client: client, model: "m", cfg: &config.Config{MaxHistory: 10, ActivePersona: "samantha", AgentName: "Samantha"}}

	reply, err := o.ThinkFull(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkFull() error = %v", err)
	}
	if strings.Contains(reply, "Historical assistant") || strings.Contains(reply, "OLDRESPONSE1") {
		t.Fatalf("full reply leaked marker: %q", reply)
	}
	if !strings.Contains(reply, "Sure thing.") || !strings.Contains(reply, "I am Obey.") {
		t.Fatalf("full reply lost text: %q", reply)
	}
	messages := o.buildMessages("")
	last := messages[len(messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Samantha said:") || strings.Contains(last.Content, "OLDRESPONSE1") {
		t.Fatalf("next request gave contaminated output assistant authority: %+v", last)
	}
	canonical := o.CanonicalHistory()
	if len(canonical) == 0 || !strings.Contains(canonical[len(canonical)-1].Content, "OLDRESPONSE1") {
		t.Fatalf("canonical history lost marker evidence: %+v", canonical)
	}
}

func markerStreamClient(t *testing.T, lines []string) *api.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, line := range lines {
			_, _ = w.Write([]byte(line + "\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	return api.NewClient(base, http.DefaultClient)
}

func TestHistoricalMarkerFilterEveryChunkBoundary(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"Before [Historical assistant: Obey response ID123] after.", "Before  after."},
		{"[Historical assistant: ]", "[Historical assistant: ]"},
		{"Before [Historical assistant: ordinary\nquotation] after.", "Before [Historical assistant: ordinary\nquotation] after."},
		{"[Historical assistant: Obey response ID1][Historical assistant: Obey response ID2]Hi", "Hi"},
		{"[Historical assistant: Obey response ID1]\n[Historical assistant: Obey response ID2]\nYeah…", "Yeah…"},
		{"[Historical assistant: Obey response ID1]\r\n[Historical assistant: Obey response ID2]\r\nYeah…", "Yeah…"},
		{"[Historical assistant: Obey response ID1]\r", "\r"},
		{"[Historical assistant: Obey response ID1]\rYeah…", "\rYeah…"},
		{"[Historical assistant: Obey response ID1]\n\nYeah…", "\nYeah…"},
		{"[Historical assistant: Obey response ID1]", ""},
		{"[Historical assistant: ]\nYeah…", "[Historical assistant: ]\nYeah…"},
	} {
		var byteFilter historicalAssistantMarkerFilter
		var byteOutput strings.Builder
		for i := range len(tc.input) {
			byteOutput.WriteString(byteFilter.Feed(tc.input[i : i+1]))
		}
		byteOutput.WriteString(byteFilter.Flush())
		if got := byteOutput.String(); got != tc.want {
			t.Fatalf("byte chunks of %q: got %q, want %q", tc.input, got, tc.want)
		}
		for split := 0; split <= len(tc.input); split++ {
			var filter historicalAssistantMarkerFilter
			got := filter.Feed(tc.input[:split]) + filter.Feed(tc.input[split:]) + filter.Flush()
			if got != tc.want {
				t.Fatalf("split %d of %q: got %q, want %q", split, tc.input, got, tc.want)
			}
		}
	}
}

func TestThinkStreamConsumesStackedMarkerNewlines(t *testing.T) {
	client := markerStreamClient(t, []string{
		`{"model":"m","message":{"role":"assistant","content":"[Historical assistant: Obey response ID1]"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"\r"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"\n[Historical assistant: Obey response ID2]"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"\nYeah…"},"done":true,"done_reason":"stop"}`,
	})
	o := &OllamaBrain{client: client, model: "m", cfg: &config.Config{MaxHistory: 10}}
	stream, err := o.ThinkStream(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spoken, _ := drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil {
		t.Fatal(result.Err)
	}
	if spoken != "Yeah…" {
		t.Fatalf("speech received marker whitespace: %q", spoken)
	}
}
