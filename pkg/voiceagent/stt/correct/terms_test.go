package correct

import (
	"reflect"
	"testing"
)

func TestParseTerm(t *testing.T) {
	tests := []struct {
		in      string
		want    Term
		wantErr bool
	}{
		{"", Term{}, true},
		{"   ", Term{}, true},
		{"!!!", Term{}, true},
		{"=alias", Term{}, true},
		{"Samantha", Term{Text: "Samantha"}, false},
		{"Ollama!", Term{Text: "Ollama", Fuzzy: true}, false},
		{"Claude=clawed, cloud", Term{Text: "Claude", Aliases: []string{"clawed", "cloud"}}, false},
		{"Uncle Fu!=uncle few", Term{Text: "Uncle Fu", Aliases: []string{"uncle few"}, Fuzzy: true}, false},
		{" obey-voice ", Term{Text: "obey-voice"}, false},
		{"Grok=,,", Term{Text: "Grok"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseTerm(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	if _, err := ParseTerms([]string{"Samantha", ""}); err == nil {
		t.Fatal("ParseTerms should fail on an empty entry")
	}
}

func TestFuzzyEligible(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"Samantha", true}, {"Veronica", true}, {"Jarvis", true}, {"Ollama", true},
		{"Santa", false}, {"Joker", false}, {"Obey", false}, {"festival", false},
		{"Grok", false}, {"Qwen", false}, {"worked", false}, {"", false},
	}
	for _, tc := range tests {
		if got := FuzzyEligible(tc.text); got != tc.want {
			t.Errorf("FuzzyEligible(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestPersonaTerms(t *testing.T) {
	got := PersonaTerms([]string{"Samantha", " ", "Santa", "Veronica Hype", "!!"})
	want := []Term{{Text: "Samantha", Fuzzy: true}, {Text: "Santa"}, {Text: "Veronica Hype", Fuzzy: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMerge(t *testing.T) {
	explicit := []Term{{Text: "Samantha!", Fuzzy: true}, {Text: "samantha"}}
	derived := []Term{{Text: "Samantha", Fuzzy: false}, {Text: "Jarvis", Fuzzy: true}}
	got, dropped := Merge(explicit, derived)
	if dropped != 0 || len(got) != 2 || got[0].Text != "Samantha!" || got[1].Text != "Jarvis" {
		t.Fatalf("got %+v dropped %d", got, dropped)
	}

	many := make([]Term, MaxTerms+5)
	for i := range many {
		many[i] = Term{Text: "term" + string(rune('a'+i%26)) + string(rune('a'+i/26))}
	}
	got, dropped = Merge(nil, many)
	if len(got) != MaxTerms || dropped != 5 {
		t.Fatalf("cap: len %d dropped %d", len(got), dropped)
	}
}
