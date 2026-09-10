package main

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBatchOutput(t *testing.T) {
	byPath := map[string]fixture{
		"/f/a.wav": {wav: "/f/a.wav", expected: "hello world"},
		"/f/b.wav": {wav: "/f/b.wav"},
	}
	header := `{"type":"batch_header","load_ms":812.5}`
	lineA := `{"file":"/f/a.wav","text":"hello world","mel_ms":1.5,"encode_ms":10.25,"decode_ms":16.35}`
	lineB := `{"file":"/f/b.wav","text":"","mel_ms":1,"encode_ms":2,"decode_ms":3}`

	tests := []struct {
		name    string
		out     string
		byPath  map[string]fixture
		wantErr string
	}{
		{
			name:    "unknown file",
			out:     header + "\n" + `{"file":"/f/z.wav","text":"x"}` + "\n" + lineB + "\n",
			byPath:  byPath,
			wantErr: "unknown file /f/z.wav",
		},
		{
			name:    "count mismatch",
			out:     header + "\n" + lineA + "\n",
			byPath:  byPath,
			wantErr: "expected 2 batch results, got 1",
		},
		{
			name:    "malformed line",
			out:     header + "\nnot json\n",
			byPath:  byPath,
			wantErr: "parse batch line",
		},
		{
			name:   "header and file lines",
			out:    header + "\n" + lineA + "\n" + lineB + "\n",
			byPath: byPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBatchOutput([]byte(tt.out), tt.byPath, "cand", io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("got %d results, want 2", len(got))
			}
			// The header's load_ms never reaches a result: 1.5+10.25+16.35 ms.
			if got[0].computeNS != 28100000 {
				t.Errorf("computeNS = %d, want 28100000", got[0].computeNS)
			}
			if got[0].text != "hello world" || got[0].fix.expected != "hello world" {
				t.Errorf("result[0] = %+v", got[0])
			}
			if got[1].computeNS != 6000000 {
				t.Errorf("computeNS = %d, want 6000000", got[1].computeNS)
			}
		})
	}
}

func TestMakeRecord(t *testing.T) {
	tests := []struct {
		name          string
		fix           fixture
		text          string
		wantProvider  string
		wantExpected  string
		wantScore     float64
		wantNoScoring bool
	}{
		{
			name:          "nonspeech is segregated and unscored",
			fix:           fixture{wav: "/f/nonspeech-silence.wav"},
			text:          "",
			wantProvider:  "cand-nonspeech",
			wantNoScoring: true,
		},
		{
			name:          "nonspeech hallucination still unscored",
			fix:           fixture{wav: "/f/nonspeech-noise.wav"},
			text:          "you",
			wantProvider:  "cand-nonspeech",
			wantNoScoring: true,
		},
		{
			name:         "speech is scored",
			fix:          fixture{wav: "/f/fix01.wav", expected: "hello world"},
			text:         "hello world",
			wantProvider: "cand",
			wantExpected: "hello world",
			wantScore:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := makeRecord("cand", 2, batchResult{fix: tt.fix, text: tt.text, computeNS: 5})
			if r.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", r.Provider, tt.wantProvider)
			}
			if r.Expected != tt.wantExpected {
				t.Errorf("Expected = %q, want %q", r.Expected, tt.wantExpected)
			}
			if tt.wantNoScoring && r.TranscriptScore != 0 {
				t.Errorf("TranscriptScore = %v, want 0 (excluded from accuracy)", r.TranscriptScore)
			}
			if !tt.wantNoScoring && r.TranscriptScore != tt.wantScore {
				t.Errorf("TranscriptScore = %v, want %v", r.TranscriptScore, tt.wantScore)
			}
			if r.Mode != "stt" || r.Iteration != 2 || r.ElapsedNS != 5 || r.Metrics.STTFinalElapsed != 5 {
				t.Errorf("record = %+v", r)
			}
		})
	}
}

// TestTranscriptScoreParity pins the local scorer to cmd/obey-voice/cmd's
// transcriptScore. The want values are not hand-derived: they were printed by a
// throwaway test in package cmd that called the real transcriptScore on these
// exact pairs (`fmt.Printf("%.17g", transcriptScore(a, b))`). Regenerate the
// same way if benchmark.go's scoring ever changes — a candidate scored
// differently from the control makes the accuracy gap meaningless.
func TestTranscriptScoreParity(t *testing.T) {
	tests := []struct {
		expected string
		actual   string
		want     float64
	}{
		{"", "", 1},
		{"", "hello", 0},
		{"hello world", "", 0},
		{"hello world", "hello world", 1},
		{"Hello, world!", "hello world", 1},
		{"hello world", "HELLO   WORLD", 1},
		{"the quick brown fox", "the quick brown fox jumps", 0.88888888888888895},
		{"the quick brown fox jumps", "the quick brown fox", 0.88888888888888895},
		{"one two three", "four five six", 0},
		{"echo echo echo", "echo", 0.5},
		{"echo", "echo echo echo", 0.5},
		{"turn the lights on at 7", "turn the lights on at 7", 1},
		{"turn the lights on at 7", "turn the light on at seven", 0.66666666666666663},
		{"it's a test", "its a test", 0.57142857142857151},
	}

	for _, tt := range tests {
		got := transcriptScore(tt.expected, tt.actual)
		if math.Abs(got-tt.want) > 1e-12 {
			t.Errorf("transcriptScore(%q, %q) = %v, want %v", tt.expected, tt.actual, got, tt.want)
		}
	}
}

func TestEnsureWAVRecipeIdentity(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "fix01.wav")
	write := func() error { return os.WriteFile(wav, []byte("audio"), 0o644) }

	synths := 0
	count := func() error { synths++; return write() }

	if err := ensureWAV(wav, speechRecipe("hello"), count); err != nil {
		t.Fatal(err)
	}
	if synths != 1 {
		t.Fatalf("first call synthesized %d times, want 1", synths)
	}

	tests := []struct {
		name      string
		recipe    string
		prepare   func(t *testing.T)
		wantSynth bool
	}{
		{
			name:      "missing sidecar re-synthesizes",
			recipe:    speechRecipe("hello"),
			prepare:   func(t *testing.T) { os.Remove(sidecarPath(wav)) },
			wantSynth: true,
		},
		{
			name:   "corrupt sidecar re-synthesizes",
			recipe: speechRecipe("hello"),
			prepare: func(t *testing.T) {
				if err := os.WriteFile(sidecarPath(wav), []byte("deadbeef\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantSynth: true,
		},
		{
			name:      "missing wav re-synthesizes",
			recipe:    speechRecipe("hello"),
			prepare:   func(t *testing.T) { os.Remove(wav) },
			wantSynth: true,
		},
		{
			name:      "changed text re-synthesizes",
			recipe:    speechRecipe("goodbye"),
			wantSynth: true,
		},
		{
			name:      "unchanged recipe is skipped",
			recipe:    speechRecipe("goodbye"),
			wantSynth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prepare != nil {
				tt.prepare(t)
			}
			before := synths
			if err := ensureWAV(wav, tt.recipe, count); err != nil {
				t.Fatal(err)
			}
			if got := synths > before; got != tt.wantSynth {
				t.Errorf("synthesized = %v, want %v", got, tt.wantSynth)
			}
		})
	}
}

func TestRecipesDistinguishInputs(t *testing.T) {
	if speechRecipe("a") == speechRecipe("b") {
		t.Error("different texts share a recipe")
	}
	if nonSpeechRecipe("silence", 10) == nonSpeechRecipe("noise", 10) {
		t.Error("silence and noise share a recipe")
	}
	if !strings.Contains(speechRecipe("a"), "voice="+synthVoice) {
		t.Error("recipe does not bind the synth voice")
	}
	if !strings.Contains(speechRecipe("a"), "speed="+synthSpeed) {
		t.Error("recipe does not bind the synth speed")
	}
}

func TestReadFixtureTexts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixtures.txt")
	body := "# comment\n\nfirst line\n  second line  \n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := readFixtureTexts(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("missing fixture list: want error")
	}

	got, err := readFixtureTexts(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first line", "second line"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}
