package scene

import (
	"os"
	"path/filepath"
	"testing"
)

func castMember() CastRuntime {
	return CastRuntime{
		ID:        "sam",
		PersonaID: "samantha",
		Template:  SynthTemplate{Provider: "qwen3-tts", Tier: "0.6b", Voice: "Vivian", Language: "English"},
	}
}

// Every input that changes the audio has to change the key, or a resume mixes
// one conversation out of two different renderings.
func TestSceneTurnResumeKeyCoversTheAudio(t *testing.T) {
	base := castMember()
	const text, out = "You're late.", "turns/000-sam.wav"
	want := turnResumeKey(base, text, out)

	changed := map[string]func(*CastRuntime) (string, string){
		"persona":         func(c *CastRuntime) (string, string) { c.PersonaID = "uncle-fu"; return text, out },
		"provider":        func(c *CastRuntime) (string, string) { c.Template.Provider = "kokoro"; return text, out },
		"tier":            func(c *CastRuntime) (string, string) { c.Template.Tier = "1.7b"; return text, out },
		"voice":           func(c *CastRuntime) (string, string) { c.Template.Voice = "Uncle_Fu"; return text, out },
		"language":        func(c *CastRuntime) (string, string) { c.Template.Language = "Chinese"; return text, out },
		"mode":            func(c *CastRuntime) (string, string) { c.Template.Mode = "customvoice"; return text, out },
		"instruction":     func(c *CastRuntime) (string, string) { c.Template.Instruction = "warmly"; return text, out },
		"reference audio": func(c *CastRuntime) (string, string) { c.Template.ReferenceAudio = "/ref.wav"; return text, out },
		"reference text":  func(c *CastRuntime) (string, string) { c.Template.ReferenceTranscript = "hi"; return text, out },
		"the line itself": func(*CastRuntime) (string, string) { return "You are late.", out },
		"the output it lands": func(*CastRuntime) (string, string) {
			return text, "turns/001-sam.wav"
		},
	}
	for name, mutate := range changed {
		t.Run(name, func(t *testing.T) {
			member := base
			nextText, nextOut := mutate(&member)
			if got := turnResumeKey(member, nextText, nextOut); got == want {
				t.Fatalf("changing %s left the resume key unchanged", name)
			}
		})
	}

	if turnResumeKey(base, text, out) != want {
		t.Error("the same inputs produced two different resume keys")
	}
}

// Fields are domain-separated, so text spilling into an adjacent field cannot
// forge a match.
func TestSceneTurnResumeKeyFieldsAreSeparated(t *testing.T) {
	a := castMember()
	a.Template.Voice = "Vivian"
	a.Template.Language = "English"

	b := castMember()
	b.Template.Voice = "VivianEnglish"
	b.Template.Language = ""

	if turnResumeKey(a, "hi", "out") == turnResumeKey(b, "hi", "out") {
		t.Fatal("two different mouths share a resume key")
	}
}

func TestSceneResumableRefusesUnusableTurns(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "000-sam.wav")
	if err := os.WriteFile(wavPath, []byte("RIFF"), 0o600); err != nil {
		t.Fatalf("writing wav: %v", err)
	}
	const key = "abc123"

	cases := map[string]struct {
		prior RunTurn
		path  string
		want  bool
	}{
		"complete and unchanged": {RunTurn{Status: StatusComplete, ResumeKey: key}, wavPath, true},
		"skipped last time":      {RunTurn{Status: StatusSkipped, ResumeKey: key}, wavPath, true},
		"failed last time":       {RunTurn{Status: StatusFailed, ResumeKey: key}, wavPath, false},
		"different key":          {RunTurn{Status: StatusComplete, ResumeKey: "other"}, wavPath, false},
		"no key at all":          {RunTurn{Status: StatusComplete}, wavPath, false},
		"wav is gone":            {RunTurn{Status: StatusComplete, ResumeKey: key}, filepath.Join(dir, "gone.wav"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := resumable(tc.prior, key, tc.path); got != tc.want {
				t.Fatalf("resumable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSceneRunManifestCounts(t *testing.T) {
	m := RunManifest{Turns: []RunTurn{
		{Status: StatusComplete}, {Status: StatusComplete},
		{Status: StatusSkipped},
		{Status: StatusFailed},
	}}
	complete, skipped, failed := m.Counts()
	if complete != 2 || skipped != 1 || failed != 1 {
		t.Fatalf("Counts() = %d/%d/%d, want 2/1/1", complete, skipped, failed)
	}
}

func TestSceneWriteRunManifestRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := ManifestPath(filepath.Join(dir, "out"))
	m := RunManifest{
		Schema:       RunSchema,
		Mode:         ModeVerbatim,
		Conversation: ConversationFile,
		SampleRate:   24000,
		PauseMS:      400,
		Cast:         []RunCast{{ID: "sam", PersonaID: "samantha", TTSProvider: "kokoro", TTSVoice: "af_heart"}},
		Turns:        []RunTurn{{Index: 0, ID: "000-sam", Speaker: "sam", Text: "hi", WAV: "turns/000-sam.wav", DurationMS: 820, Status: StatusComplete}},
	}
	if err := WriteRunManifest(path, m); err != nil {
		t.Fatalf("WriteRunManifest: %v", err)
	}

	loaded, ok := LoadRunManifest(path)
	if !ok {
		t.Fatal("the manifest just written did not load back")
	}
	if loaded.Schema != m.Schema || len(loaded.Turns) != 1 || loaded.Turns[0].WAV != "turns/000-sam.wav" {
		t.Errorf("round trip = %+v, want the written manifest", loaded)
	}
	// A run that leaves no temp files behind is one a later --resume can trust.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("reading out dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != ManifestFile {
		t.Errorf("out dir holds %v, want only %s", entries, ManifestFile)
	}
}

// A missing or corrupt manifest is not a resume: every turn is treated as new
// rather than guessed at.
func TestSceneLoadRunManifestRejectsUnusable(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing corrupt manifest: %v", err)
	}
	cases := map[string]string{
		"missing": filepath.Join(dir, "nope.json"),
		"corrupt": corrupt,
		"empty":   "",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := LoadRunManifest(path); ok {
				t.Fatal("LoadRunManifest accepted a manifest it cannot trust")
			}
		})
	}
}
