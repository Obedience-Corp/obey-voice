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
		`{"type":"session_start","ts":"2026-09-05T10:00:00Z","stt":"parakeet"}`,
		`{"type":"utterance","offset_ms":1000,"text":"use coco row for tts"}`,
		`{"type":"utterance","offset_ms":2000,"text":"tell veronika"}`,
		`{"type":"speaker_utterance","offset_ms":1000,"label":"Lance","text":"use coco row for tts"}`,
		`{"type":"speaker_utterance","offset_ms":2000,"label":"Lance","text":"tell veronika about olama"}`,
		`{"type":"speaker_utterance","offset_ms":3000,"label":"Lance","text":"Um, we have thirty five people and, uh, five percent churn."}`,
		`{"type":"note","text":"olama"}`,
		`{"type":"session_end","ts":"2026-09-05T10:05:00Z"}`,
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

func runSTTCmd(t *testing.T, cfg *config.Config, args ...string) string {
	t.Helper()
	cmd := newSTTCmd(func() (*config.Config, error) { c := *cfg; return &c, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("stt %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestSTTAuditReplaysSessionsAndMeetings(t *testing.T) {
	cfg := writeSTTAuditFixtures(t)
	var res sttAuditResult
	if err := json.Unmarshal([]byte(runSTTCmd(t, cfg, "audit", "--json")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Schema != sttAuditSchemaV1 {
		t.Fatalf("schema = %q", res.Schema)
	}
	if strings.Join(res.Stages, ",") != "custom_words" {
		t.Fatalf("stages = %v", res.Stages)
	}
	want := sttAuditCorpus{Sessions: 1, SessionUtterances: 2, Meetings: 1, MeetingUtterances: 3}
	if res.Corpus != want {
		t.Fatalf("corpus = %+v, want %+v", res.Corpus, want)
	}
	if res.Utterances != 5 || res.Changed != 3 {
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
	if len(res.Fillers) != 0 || len(res.Numbers) != 0 {
		t.Fatalf("stages off should report nothing: fillers %v numbers %v", res.Fillers, res.Numbers)
	}
}

func TestSTTAuditScopesAndOverrides(t *testing.T) {
	cfg := writeSTTAuditFixtures(t)

	var res sttAuditResult
	if err := json.Unmarshal([]byte(runSTTCmd(t, cfg, "audit", "--json", "--sessions")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Corpus.Meetings != 0 || res.Corpus.Sessions != 1 || res.Changed != 2 {
		t.Fatalf("sessions only: %+v changed %d", res.Corpus, res.Changed)
	}

	if err := json.Unmarshal([]byte(runSTTCmd(t, cfg, "audit", "--json", "--meetings", "--threshold", "0.5", "--terms", "Fu=tts")), &res); err != nil {
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

	text := runSTTCmd(t, cfg, "audit", "--sessions")
	for _, want := range []string{"Transcript corrections audit", "olama", "Ollama", "clawed", "Claude", "1 session(s) with 2 user turn(s)", "2 of 2 utterance(s) would change"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "veronika") {
		t.Fatalf("sessions-only output mentions a meeting phrase:\n%s", text)
	}
}

func TestSTTAuditFillersAndNumbers(t *testing.T) {
	cfg := writeSTTAuditFixtures(t)
	var res sttAuditResult
	if err := json.Unmarshal([]byte(runSTTCmd(t, cfg, "audit", "--json", "--meetings", "--fillers", "--numbers")), &res); err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Stages, ",") != "fillers,custom_words,numbers" {
		t.Fatalf("stages = %v", res.Stages)
	}
	if res.Changed != 2 {
		t.Fatalf("changed = %d", res.Changed)
	}
	fillers := map[string]int{}
	for _, r := range res.Fillers {
		fillers[r.Phrase] = r.Count
	}
	if fillers["um"] != 1 || fillers["uh"] != 1 {
		t.Fatalf("fillers = %+v", res.Fillers)
	}
	numbers := map[string]string{}
	for _, r := range res.Numbers {
		numbers[r.Phrase] = r.Term
	}
	if numbers["thirty five"] != "35" || numbers["five percent"] != "5%" {
		t.Fatalf("numbers = %+v", res.Numbers)
	}
	text := runSTTCmd(t, cfg, "audit", "--meetings", "--fillers", "--numbers")
	for _, want := range []string{"Fillers removed", "Numbers converted", "thirty five", "35"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output lacks %q:\n%s", want, text)
		}
	}
}

func TestSTTPreview(t *testing.T) {
	cfg := writeSTTAuditFixtures(t)
	bundle := filepath.Join(config.MeetingsDirFrom(cfg), "standup.meeting")

	diff := runSTTCmd(t, cfg, "preview", "--diff", "--fillers", "--numbers", bundle)
	for _, want := range []string{"tell veronika about olama", "tell Veronica about Ollama", "We have 35 people and 5% churn.", "2 utterance(s) changed"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff output lacks %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "coco row") {
		t.Fatalf("diff printed an unchanged utterance:\n%s", diff)
	}

	rendered := runSTTCmd(t, cfg, "preview", "--fillers", "--numbers", bundle)
	for _, want := range []string{"We have 35 people and 5% churn.", "tell Veronica about Ollama", "nothing written"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered note lacks %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "thirty five") {
		t.Fatalf("rendered note still has the spoken number:\n%s", rendered)
	}

	cmd := newSTTCmd(func() (*config.Config, error) { return &config.Config{}, nil })
	cmd.SetArgs([]string{"preview", bundle})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no correction stage is enabled") {
		t.Fatalf("preview with nothing enabled: err = %v", err)
	}
}
