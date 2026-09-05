package correct

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func testTerms(t *testing.T) []Term {
	t.Helper()
	terms, err := ParseTerms([]string{
		"Samantha!", "Veronica!", "Veronica Hype!", "Jarvis!", "Kokoro!", "Uncle Fu!",
		"Ollama!", "obey-voice", "workitem!", "Claude!=clawed", "Grok=grock",
		"Qwen=quen", "Santa!", "festival", "Obey", "Parakeet",
	})
	if err != nil {
		t.Fatalf("ParseTerms: %v", err)
	}
	return terms
}

func TestCustomWordsScan(t *testing.T) {
	cw := NewCustomWords(testTerms(t), 0)
	tests := []struct {
		name, in, want string
		candidates     int
	}{
		{"empty", "", "", 0},
		{"no terms present", "the quick brown fox", "the quick brown fox", 0},
		{"split word exact", "hey sam antha", "hey Samantha", 0},
		{"fuzzy misspelling", "tell veronika to stop", "tell Veronica to stop", 0},
		{"longest exact wins", "veronica hype persona", "Veronica Hype persona", 0},
		{"hyphen restored, neighbor kept", "Obey Voice as usual", "Obey-voice as usual", 0},
		{"article kept", "a festival", "a festival", 0},
		{"common word never becomes jargon", "worked on it", "worked on it", 0},
		{"cloud stays cloud", "cloud storage", "cloud storage", 0},
		{"alias exact", "ask clawed to review", "ask Claude to review", 0},
		{"alias short term", "switch to grock", "switch to Grok", 0},
		{"alias mid sentence", "try the quen model", "try the Qwen model", 0},
		{"candidate not applied", "use coco row for tts", "use coco row for tts", 1},
		{"soundex boost", "run it on olama", "run it on Ollama", 0},
		{"initial vowel changed", "Restart Alama and try again", "Restart Ollama and try again", 0},
		{"initial vowel split", "Pull it with a Lama first", "Pull it with Ollama first", 0},
		{"all common words miss", "ask uncle few about it", "ask uncle few about it", 0},
		{"split exact three chars", "the para keet bundle", "the Parakeet bundle", 0},
		{"split exact sentence start", "jar vis are you there", "Jarvis are you there", 0},
		{"all caps kept", "SAMANTHA", "SAMANTHA", 0},
		{"case canonicalized", "samantha", "Samantha", 0},
		{"punctuation kept", "Hey, veronika!", "Hey, Veronica!", 0},
		{"common persona name exact only", "sent a letter", "sent a letter", 0},
		{"whitespace preserved", "a  b\tc", "a  b\tc", 0},
		{"multi line", "veronika\nsam antha", "Veronica\nSamantha", 0},
		{"edge guard keeps neighbor", "veronika x", "Veronica x", 0},
		{"no span across clause end", "sam, antha", "sam, antha", 0},
		{"non ascii span skipped", "verónika", "verónika", 0},
		{"surname after a proper noun kept", "Harisuddin Samanathan is building it", "Harisuddin Samanathan is building it", 0},
		{"surname before a proper noun kept", "meet samanathan Harisuddin today", "meet samanathan Harisuddin today", 0},
		{"proper noun across a clause does not guard", "Harisuddin, samanathan", "Harisuddin, Samantha", 0},
		{"capitalized common word does not guard", "Hey veronika", "Hey Veronica", 0},
		{"fuzzy after a plain word", "hey samanathan", "hey Samantha", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, cands := cw.Scan(tc.in)
			if got != tc.want {
				t.Fatalf("Scan(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if len(cands) != tc.candidates {
				t.Fatalf("candidates = %v, want %d", cands, tc.candidates)
			}
		})
	}
}

func TestCustomWordsCandidateBand(t *testing.T) {
	cw := NewCustomWords(testTerms(t), 0)
	cands := cw.Candidates("use coco row for tts")
	if len(cands) != 1 || cands[0].Span != "coco row" || cands[0].Term != "Kokoro" {
		t.Fatalf("candidates = %+v", cands)
	}
	if cands[0].Score < DefaultThreshold || cands[0].Score >= CandidateBand {
		t.Fatalf("score %v outside band", cands[0].Score)
	}
	loose := NewCustomWords(testTerms(t), 0.5)
	if got, _, _ := loose.Scan("use coco row for tts"); got != "use Kokoro for tts" {
		t.Fatalf("loose threshold: %q", got)
	}
}

func TestCustomWordsReplacements(t *testing.T) {
	cw := NewCustomWords(testTerms(t), 0)
	_, reps, _ := cw.Scan("ask clawed about veronika")
	if len(reps) != 2 {
		t.Fatalf("replacements = %+v", reps)
	}
	if !reps[0].Exact || reps[0].Span != "clawed" || reps[0].Term != "Claude" {
		t.Fatalf("first = %+v", reps[0])
	}
	if reps[1].Exact || reps[1].Span != "veronika" || reps[1].Term != "Veronica" || reps[1].Score >= DefaultThreshold {
		t.Fatalf("second = %+v", reps[1])
	}
}

func TestCustomWordsNoRematchInsideReplacement(t *testing.T) {
	terms, err := ParseTerms([]string{"Samantha=sam", "Prime=samantha"})
	if err != nil {
		t.Fatal(err)
	}
	cw := NewCustomWords(terms, 0)
	if got, _, _ := cw.Scan("sam"); got != "Samantha" {
		t.Fatalf("got %q", got)
	}
}

func TestCustomWordsFuzzyRequiresEligibility(t *testing.T) {
	terms, err := ParseTerms([]string{"Santa!", "Obey!", "Samantha!"})
	if err != nil {
		t.Fatal(err)
	}
	cw := NewCustomWords(terms, 0)
	if len(cw.fuzzy) != 1 || cw.fuzzy[0].text != "Samantha" {
		t.Fatalf("fuzzy terms = %+v", cw.fuzzy)
	}
}

func TestCustomWordsEmpty(t *testing.T) {
	var nilStage *CustomWords
	if !nilStage.Empty() {
		t.Fatal("nil stage should be empty")
	}
	if got, _, _ := nilStage.Scan("x"); got != "x" {
		t.Fatalf("nil Scan = %q", got)
	}
	if !NewCustomWords(nil, 0).Empty() {
		t.Fatal("no terms should be empty")
	}
}

func TestCustomWordsApplyContext(t *testing.T) {
	cw := NewCustomWords(testTerms(t), 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := cw.Apply(ctx, "veronika")
	if !errors.Is(err, context.Canceled) || got != "veronika" {
		t.Fatalf("Apply on cancelled ctx = %q, %v", got, err)
	}
	got, err = cw.Apply(context.Background(), "veronika")
	if err != nil || got != "Veronica" {
		t.Fatalf("Apply = %q, %v", got, err)
	}
}

func TestCoerceCase(t *testing.T) {
	tests := []struct{ term, span, want string }{
		{"obey-voice", "Obey Voice", "Obey-voice"},
		{"obey-voice", "OBEY VOICE", "OBEY-VOICE"},
		{"obey-voice", "obey voice", "obey-voice"},
		{"Samantha", "samantha", "Samantha"},
		{"Samantha", "SAMANTHA", "SAMANTHA"},
		{"iPhone", "Iphone", "iPhone"},
		{"x", "X", "X"},
	}
	for _, tc := range tests {
		if got := coerceCase(tc.term, tc.span); got != tc.want {
			t.Errorf("coerceCase(%q, %q) = %q, want %q", tc.term, tc.span, got, tc.want)
		}
	}
}

func BenchmarkCustomWords(b *testing.B) {
	terms := make([]Term, 0, MaxTerms)
	for i := 0; i < MaxTerms; i++ {
		terms = append(terms, Term{Text: "Zyx" + strings.Repeat("qv", i%4+1) + string(rune('a'+i%26)) + "on", Fuzzy: true})
	}
	terms[0] = Term{Text: "Veronica", Fuzzy: true}
	cw := NewCustomWords(terms, 0)
	if len(cw.fuzzy) < MaxTerms/2 {
		b.Fatalf("expected most synthetic terms to be fuzzy, got %d", len(cw.fuzzy))
	}
	text := strings.Repeat("the meeting with veronika about the roadmap ran long ", 5)
	b.ReportAllocs()
	for b.Loop() {
		cw.Scan(text)
	}
}
