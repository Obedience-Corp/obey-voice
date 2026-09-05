package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func writeSTTAuditFixtures(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)

	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionJSON := `{"id":"s1","turns":[
	  {"role":"user","content":"run it on olama","speaker":"speaker-1"},
	  {"role":"assistant","content":"olama is not a word"},
	  {"role":"user","content":"ask clawed to review"}]}`
	if err := os.WriteFile(filepath.Join(sessions, "s1.json"), []byte(sessionJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	internal := filepath.Join(dir, "meetings", "standup.meeting", ".samantha")
	if err := os.MkdirAll(internal, 0o755); err != nil {
		t.Fatal(err)
	}
	events := strings.Join([]string{
		`{"type":"utterance","text":"use coco row for tts"}`,
		`{"type":"utterance","text":"tell veronika"}`,
		`{"type":"speaker_utterance","label":"Lance","text":"use coco row for tts"}`,
		`{"type":"speaker_utterance","label":"Lance","text":"tell veronika about olama"}`,
		`{"type":"note","text":"olama"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(internal, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meetings", "broken.meeting"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	return &config.Config{
		STTCustomWords:             []string{"Claude=clawed", "Kokoro!", "!!!"},
		STTCustomWordsFromPersonas: true,
		PersonaDisplayNames:        []string{"Veronica", "Ollama"},
	}
}

func runSTTAuditCmd(t *testing.T, cfg *config.Config, args ...string) string {
	t.Helper()
	cmd := newSTTCmd(func() (*config.Config, error) { c := *cfg; return &c, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"audit"}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("stt audit %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestSTTAuditReplaysSessionsAndMeetings(t *testing.T) {
	cfg := writeSTTAuditFixtures(t)
	var res sttAuditResult
	if err := json.Unmarshal([]byte(runSTTAuditCmd(t, cfg, "--json")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Schema != sttAuditSchemaV1 {
		t.Fatalf("schema = %q", res.Schema)
	}
	want := sttAuditCorpus{Sessions: 1, SessionUtterances: 2, Meetings: 1, MeetingUtterances: 2}
	if res.Corpus != want {
		t.Fatalf("corpus = %+v, want %+v", res.Corpus, want)
	}
	if res.Utterances != 4 || res.Changed != 3 {
		t.Fatalf("utterances %d changed %d", res.Utterances, res.Changed)
	}
	if got := res.IgnoredEntries; len(got) != 1 || got[0] != "!!!" {
		t.Fatalf("ignored = %v", got)
	}
	if got := res.FuzzyTerms; strings.Join(got, ",") != "Kokoro,Veronica,Ollama" {
		t.Fatalf("fuzzy = %v", got)
	}
	rows := map[string]sttAuditRow{}
	for _, r := range res.Replacements {
		rows[r.Phrase+"→"+r.Term] = r
	}
	if r := rows["olama→Ollama"]; r.Kind != "fuzzy" || r.Count != 2 || r.Score <= 0 || r.Score >= 0.12 {
		t.Fatalf("olama row = %+v", r)
	}
	if r := rows["clawed→Claude"]; r.Kind != "alias" || r.Count != 1 || r.Example != "ask clawed to review" {
		t.Fatalf("clawed row = %+v", r)
	}
	if r := rows["veronika→Veronica"]; r.Kind != "fuzzy" || r.Count != 1 {
		t.Fatalf("veronika row = %+v", r)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Phrase != "coco row" || res.Candidates[0].Term != "Kokoro" || res.Candidates[0].Kind != "candidate" {
		t.Fatalf("candidates = %+v", res.Candidates)
	}
}

func TestSTTAuditScopesAndOverrides(t *testing.T) {
	cfg := writeSTTAuditFixtures(t)

	var res sttAuditResult
	if err := json.Unmarshal([]byte(runSTTAuditCmd(t, cfg, "--json", "--sessions")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Corpus.Meetings != 0 || res.Corpus.Sessions != 1 || res.Changed != 2 {
		t.Fatalf("sessions only: %+v changed %d", res.Corpus, res.Changed)
	}

	if err := json.Unmarshal([]byte(runSTTAuditCmd(t, cfg, "--json", "--meetings", "--threshold", "0.5", "--terms", "Fu=tts")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Corpus.Sessions != 0 || res.Threshold != 0.5 {
		t.Fatalf("meetings only: %+v threshold %v", res.Corpus, res.Threshold)
	}
	var kokoro, fu bool
	for _, r := range res.Replacements {
		kokoro = kokoro || (r.Phrase == "coco row" && r.Kind == "fuzzy")
		fu = fu || (r.Phrase == "tts" && r.Term == "Fu" && r.Kind == "alias")
	}
	if !kokoro || !fu || len(res.Candidates) != 0 {
		t.Fatalf("overrides not applied: %+v", res.Replacements)
	}

	text := runSTTAuditCmd(t, cfg, "--sessions")
	for _, want := range []string{"Custom words audit", "olama", "Ollama", "clawed", "Claude", "1 session(s) with 2 user turn(s)", "2 of 2 utterance(s) would change"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "veronika") {
		t.Fatalf("sessions-only output mentions a meeting phrase:\n%s", text)
	}
}
