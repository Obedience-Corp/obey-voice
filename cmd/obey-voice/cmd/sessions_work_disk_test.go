package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/workjournal"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/spf13/cobra"
)

func inspectionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := sessionsEnv(t)
	id := "weather-fixture"
	scope := work.WorkScope{SessionID: id, RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	outcome := work.Outcome{Scope: scope, JobID: "weather-job", ResultID: "weather-result", Status: work.OutcomeSucceeded, Text: "Fixture forecast: clear", Origin: "agency", Config: map[string]string{"persona": "samantha", "worker_plan": "nested-secret"}}
	snapshot := work.Snapshot{Scope: scope, Revision: 2, PersistedRevision: 2, Jobs: []work.JobView{{Key: work.JobKey{Scope: scope, JobID: "weather-job"}, State: work.JobTerminal, Task: "fixture weather", Outcome: &outcome}}, Pending: []work.ResultView{{Outcome: outcome, State: work.Held}}}
	writeSessionFile(t, root, session.Session{FormatVersion: session.CurrentFormatVersion, ID: id, SelectedPersonaID: "santa", SelectedPersonaDisplayName: "Santa", UpdatedAt: time.Now().Add(-time.Minute), Work: &snapshot})
	dir := filepath.Join(root, id+".work")
	if err := os.MkdirAll(filepath.Join(dir, "details"), 0700); err != nil {
		t.Fatal(err)
	}
	jsonBytes := func(v any) []byte {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	data := []any{
		map[string]any{"kind": "spawn", "tier": "light", "state": "accepted", "task": "fixture weather", "private_config": "nested-secret"},
		map[string]any{"name": "run_command", "preview": "Fixture HTTP forecast", "outcome": "succeeded", "outcome_detail": map[string]any{"exit_code": 0, "http_status": 200}, "details": workjournal.Detail{Ref: "weather-preview", OriginalBytes: 50, RetainedBytes: 50}, "full_detail_body": "never-default-body"},
		map[string]any{"snapshot": snapshot},
		map[string]any{"stage": "projected", "result_ids": []string{"weather-result"}, "attempt_id": "attempt"},
		map[string]any{"stage": "playback_complete", "result_ids": []string{"weather-result"}, "attempt_id": "attempt"},
	}
	kinds := []string{"route.decided", "tool.finished", "work.snapshot", "work.delivery", "work.delivery"}
	var lines bytes.Buffer
	for i, d := range data {
		r := workjournal.Record{Schema: workjournal.Schema, SessionID: id, RuntimeID: "runtime", ConversationEpoch: "conversation", Seq: uint64(i + 1), At: time.Now(), Type: kinds[i], JobID: "weather-job", Data: jsonBytes(d)}
		lines.Write(jsonBytes(r))
		lines.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events-runtime-000001.jsonl"), lines.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	cp := workjournal.Checkpoint{Schema: workjournal.Schema, Authority: "diagnostic_only", Sealed: true, At: time.Now(), LastSeq: 5, Snapshot: snapshot, Health: workjournal.Health{PersistedSeq: 5, PersistedAt: time.Now()}}
	if err := os.WriteFile(filepath.Join(dir, "snapshot-runtime.json"), jsonBytes(cp), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "details", "weather-preview.txt"), []byte("retained-weather-body Authorization: Basic Zm9vOmJhcg==\nconfigured-private-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.ConfigDir(), "config.yaml"), []byte("tts_worker_token: configured-private-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, id
}

func TestSessionsWorkDiskInspectionExportAndRedaction(t *testing.T) {
	root, id := inspectionFixture(t)
	path := filepath.Join(root, id+".json")
	before, _ := os.ReadFile(path)
	configBefore, _ := os.ReadFile(filepath.Join(config.ConfigDir(), "config.yaml"))
	out, err := runSessionsCmd(t, "inspect", id, "--work", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	var v sessionsWorkView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if !v.PersistedOnly || v.Work == nil || v.SelectedPersonaID != "santa" || v.Work.Pending[0].Outcome.Config["persona"] != "samantha" || v.Source != path || v.Age == nil || *v.Age < 59 {
		t.Fatalf("inspection=%+v", v)
	}
	if strings.Contains(out, "nested-secret") || strings.Contains(out, "never-default-body") {
		t.Fatal("unsafe diagnostic data escaped")
	}
	exported := filepath.Join(t.TempDir(), "export.json")
	if text, err := runSessionsCmd(t, "export", id, "--diagnostics", "--output", exported); err != nil {
		t.Fatal(err, text)
	}
	b, err := os.ReadFile(exported)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"nested-secret", "retained-weather-body", "never-default-body", "configured-private-token"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("default export leaked %q", bad)
		}
	}
	st, _ := os.Stat(exported)
	if st.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", st.Mode())
	}
	withDetails := filepath.Join(t.TempDir(), "details.json")
	if text, err := runSessionsCmd(t, "export", id, "--output", withDetails, "--include-details"); err != nil {
		t.Fatal(err, text)
	}
	b, _ = os.ReadFile(withDetails)
	if !strings.Contains(string(b), "retained-weather-body") || strings.Contains(string(b), "Zm9vOmJhcg==") || strings.Contains(string(b), "configured-private-token") {
		t.Fatalf("unsafe/missing detail body: %s", b)
	}
	after, _ := os.ReadFile(path)
	configAfter, _ := os.ReadFile(filepath.Join(config.ConfigDir(), "config.yaml"))
	if !bytes.Equal(before, after) || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("inspection changed session/config")
	}
}

func TestSessionsWorkStatusesAndNoOverwrite(t *testing.T) {
	root, id := inspectionFixture(t)
	output := filepath.Join(t.TempDir(), "existing.json")
	_ = os.WriteFile(output, []byte("keep existing"), 0600)
	if _, err := runSessionsCmd(t, "export", id, "--output", output); err == nil {
		t.Fatal("overwrote existing output")
	}
	b, _ := os.ReadFile(output)
	if string(b) != "keep existing" {
		t.Fatal("output changed")
	}
	if _, err := runSessionsCmd(t, "export", id, "--output", filepath.Join(root, id+".json"), "--overwrite"); ExitCode(err) != 2 {
		t.Fatalf("source overwrite err=%v", err)
	}
	_ = os.RemoveAll(filepath.Join(root, id+".work"))
	out, err := runSessionsCmd(t, "inspect", id, "--json")
	if ExitCode(err) != 5 || !strings.Contains(out, "unavailable") {
		t.Fatalf("missing sidecar status=%d output=%s", ExitCode(err), out)
	}
	writeSessionFile(t, root, session.Session{ID: "future", FormatVersion: session.CurrentFormatVersion + 1})
	if _, err := runSessionsCmd(t, "inspect", "future", "--json"); ExitCode(err) != 4 {
		t.Fatalf("future version=%v", err)
	}
	if _, err := runSessionsCmd(t, "inspect", "absent", "--json"); ExitCode(err) != 3 {
		t.Fatalf("missing session=%v", err)
	}
}

func TestSessionsWorkBypassesInteractiveParentHook(t *testing.T) {
	_, id := inspectionFixture(t)
	called := false
	parent := &cobra.Command{Use: "root", PersistentPreRunE: func(*cobra.Command, []string) error { called = true; return nil }}
	parent.AddCommand(newSessionsCmd())
	parent.SetArgs([]string{"sessions", "inspect", id, "--json"})
	var output bytes.Buffer
	parent.SetOut(&output)
	parent.SetErr(&output)
	if err := parent.Execute(); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("read-only inspection invoked interactive config/seed hook")
	}
}

func TestSessionsWorkInvalidUsage(t *testing.T) {
	sessionsEnv(t)
	for _, args := range [][]string{{"inspect"}, {"events"}, {"export"}, {"inspect", "one", "two"}, {"events", "one", "--unknown-flag"}, {"export", "one"}} {
		if _, err := runSessionsCmd(t, args...); ExitCode(err) != 2 {
			t.Fatalf("args=%v status=%d err=%v", args, ExitCode(err), err)
		}
	}
}

func TestSessionsWorkUsefulPartialExportKeepsStatus(t *testing.T) {
	root, id := inspectionFixture(t)
	p := filepath.Join(root, id+".work", "events-runtime-000001.jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("malformed complete record\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "partial.json")
	if _, err := runSessionsCmd(t, "export", id, "--output", output); ExitCode(err) != 5 {
		t.Fatalf("partial export status=%d err=%v", ExitCode(err), err)
	}
	b, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(b), "weather-result") || !strings.Contains(string(b), `"incomplete": true`) {
		t.Fatalf("useful partial export absent: %v", err)
	}
}
