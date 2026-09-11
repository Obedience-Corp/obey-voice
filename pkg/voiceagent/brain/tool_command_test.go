package brain

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestCommandOutputFactsAndCompatibleText(t *testing.T) {
	for _, tc := range []struct {
		name, command, preview string
		status                 events.ToolOutcomeStatus
		bytes                  int64
		truncated              bool
	}{
		{name: "empty output", command: "true", preview: "(empty)", status: events.ToolOutcomeSucceeded},
		{name: "empty command", command: "   ", preview: "(empty)", status: events.ToolOutcomeFailed},
		{name: "literal JSON output", command: `printf '{"output":"keep this envelope"}'`, preview: `{"output":"keep this envelope"}`, status: events.ToolOutcomeSucceeded, bytes: 31},
		{name: "literal error output is successful", command: `printf 'error: a literal message'`, preview: "error: a literal message", status: events.ToolOutcomeSucceeded, bytes: 24},
		{name: "multiline", command: `printf 'first\nsecond\n'`, preview: "first…", status: events.ToolOutcomeSucceeded, bytes: 13, truncated: true},
		{name: "output prefix cap", command: `printf '%100000s' x`, preview: "(empty)", status: events.ToolOutcomeSucceeded, bytes: 100000, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &hookRecorder{}
			adapter := h.session().execute(t.Context(), t.TempDir(), runCommandCall("call", tc.command))
			if len(h.ends) != 1 || h.ends[0].Outcome == nil {
				t.Fatalf("missing terminal facts: %+v", h.ends)
			}
			end := h.ends[0]
			o := end.Outcome
			if o.Status != tc.status || o.OutputPreview != tc.preview || end.Preview != o.OutputPreview {
				t.Fatalf("outcome=%+v legacy preview=%q", o, end.Preview)
			}
			if o.OutputBytes != tc.bytes || o.Truncated != tc.truncated {
				t.Fatalf("output bytes/truncated=%d/%v want %d/%v", o.OutputBytes, o.Truncated, tc.bytes, tc.truncated)
			}
			if end.Elapsed != o.Elapsed || o.Elapsed <= 0 {
				t.Fatalf("inconsistent elapsed: %v/%v", end.Elapsed, o.Elapsed)
			}
			if o.HTTPStatus != nil {
				t.Fatalf("fabricated HTTP status %d", *o.HTTPStatus)
			}
			if tc.status == events.ToolOutcomeSucceeded {
				if end.Err != "" || o.ExitCode == nil || *o.ExitCode != 0 {
					t.Fatalf("successful command facts=%+v legacyerr=%q", o, end.Err)
				}
				var model struct{ Output, Command string }
				if err := json.Unmarshal([]byte(adapter), &model); err != nil {
					t.Fatal(err)
				}
				if model.Command != tc.command || len(model.Output) > commandOutputLimit+30 {
					t.Fatalf("model adapter changed command or is unbounded: command=%q bytes=%d", model.Command, len(model.Output))
				}
			} else if end.Err == "" || o.ExitCode != nil {
				t.Fatalf("missing input claimed execution: %+v", end)
			}
		})
	}
}

func TestCommandUnicodeCaptureAndRedaction(t *testing.T) {
	raw := strings.Repeat("界", 6000)
	h := &hookRecorder{}
	adapter := h.session().execute(t.Context(), t.TempDir(), runCommandCall("unicode", "printf '"+raw+"'"))
	end := h.ends[0]
	o := end.Outcome
	var model struct{ Output string }
	if err := json.Unmarshal([]byte(adapter), &model); err != nil {
		t.Fatal(err)
	}
	if o.OutputBytes != int64(len(raw)) || !o.Truncated || utf8.RuneCountInString(o.OutputPreview) > 240 {
		t.Fatalf("unbounded or incorrect Unicode outcome: %+v", o)
	}
	if !utf8.ValidString(o.OutputPreview) || !utf8.ValidString(model.Output) || strings.ContainsRune(model.Output, '�') {
		t.Fatalf("split UTF-8 sequence in preview or adapter")
	}
	h = &hookRecorder{}
	h.session().execute(context.Background(), t.TempDir(), runCommandCall("redact", `token=private_value; printf 'token=private_value\n'`))
	end = h.ends[0]
	if strings.Contains(h.starts[0].Summary, "private_value") || strings.Contains(end.Preview, "private_value") || strings.Contains(end.Outcome.InputSummary, "private_value") {
		t.Fatalf("known key/value secret leaked in diagnostics: start=%+v end=%+v", h.starts[0], end)
	}
	if !strings.Contains(end.Preview, "[redacted]") {
		t.Fatalf("redaction marker missing: %q", end.Preview)
	}
}

func TestOversizedCommandFailureRemainsInModelText(t *testing.T) {
	h := &hookRecorder{}
	adapter := h.session().execute(t.Context(), t.TempDir(), runCommandCall("large-failure", `printf '%100000s' x; exit 9`))
	var model struct{ Output string }
	if err := json.Unmarshal([]byte(adapter), &model); err != nil {
		t.Fatal(err)
	}
	o := h.ends[0].Outcome
	if o.Status != events.ToolOutcomeFailed || o.ExitCode == nil || *o.ExitCode != 9 || !strings.Contains(model.Output, "exit status 9") {
		t.Fatalf("model/observed failure diverged: outcome=%+v model tail=%q", o, model.Output[len(model.Output)-100:])
	}
}

func TestCommandKnownCredentialFormsAreRedacted(t *testing.T) {
	for _, command := range []string{
		`printf 'token=secret_value'`,
		`printf '{"token":"secret_value"}'`,
		`printf 'Authorization: Bearer secret_value'`,
		`printf 'password="secret_value with spaces"'`,
	} {
		h := &hookRecorder{}
		h.session().execute(t.Context(), t.TempDir(), runCommandCall("redact", command))
		end := h.ends[0]
		if strings.Contains(h.starts[0].Summary, "secret_value") || strings.Contains(end.Outcome.InputSummary, "secret_value") || strings.Contains(end.Preview, "secret_value") {
			t.Errorf("credential form leaked: command=%q summary=%q preview=%q", command, h.starts[0].Summary, end.Preview)
		}
	}
}

func TestCommandCanceledAfterProcessStarts(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	h := &hookRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.session().execute(ctx, dir, runCommandCall("running", "printf ready > ready; sleep 30"))
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			<-done
			t.Fatal("command failed to start")
		}
	}
	cancel()
	<-done
	o := h.ends[0].Outcome
	if o.Status != events.ToolOutcomeCanceled || o.ExitCode != nil || o.Signal == "" {
		t.Fatalf("running command cancellation facts: %+v", o)
	}
}
