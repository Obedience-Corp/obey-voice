package correct

import (
	"context"
	"strings"
	"testing"
)

func TestRemoveFillers(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"no fillers", "So what do you think?", "So what do you think?"},
		{"sentence start with comma", "Um, yeah, I think so.", "Yeah, I think so."},
		{"sentence start no comma", "Um just tell me a story.", "Just tell me a story."},
		{"mid sentence between commas", "So what I'll do is all, um, and then see.", "So what I'll do is all, and then see."},
		{"mid sentence bare", "Can you help me uh design a thing", "Can you help me design a thing"},
		{"after conjunction comma", "I'll reconnect with you and, uh, we'll go from there.", "I'll reconnect with you and we'll go from there."},
		{"trailing sentence", "I keep forgetting. Um...", "I keep forgetting."},
		{"closes the sentence", "I think um. Then we go.", "I think. Then we go."},
		{"never empties", "Um...", "Um..."},
		{"lone hmm stays", "Hmm.", "Hmm."},
		{"mm-hmm is an answer", "Mm-hmm.", "Mm-hmm."},
		{"uh-huh is an answer", "uh-huh, sure", "uh-huh, sure"},
		{"oh and ah stay", "Oh wow. Ah, yes I would.", "Oh wow. Ah, yes I would."},
		{"stretched spellings", "Ummm so uhhh maybe", "So maybe"},
		{"capitalizes after sentence end", "Yes. Uh, my apologies, yes.", "Yes. My apologies, yes."},
		{"triple repeat collapses", "the the the thing", "the thing"},
		{"double repeat kept", "they have people that that's what", "they have people that that's what"},
		{"repeat with case", "I I I think", "I think"},
		{"newline kept", "first line um\nsecond line", "first line\nsecond line"},
		{"multiple spaces preserved elsewhere", "a  b um c", "a  b c"},
		{"quoted filler", "\"Um, we have an idea\"", "\"We have an idea\""},
		{"deliberate repeats with commas stay", "Well, well, well, well. No, no, no, no.", "Well, well, well, well. No, no, no, no."},
		{"bare stutter collapses", "it's it's it's not technical", "it's not technical"},
		{"test test test stays", "test, test, test.", "test, test, test."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RemoveFillers(tc.in); got != tc.want {
				t.Fatalf("RemoveFillers(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestConvertNumbers(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"compound", "there are like thirty five reports.", "there are like 35 reports."},
		{"hyphenated compound", "they have thirty-five people", "they have 35 people"},
		{"currency with thousand", "cost like twenty thousand dollars.", "cost like $20,000."},
		{"year twenty twenty two", "since like twenty twenty two I've been", "since like 2022 I've been"},
		{"year nineteen ninety nine", "back in nineteen ninety nine", "back in 1999"},
		{"year with oh", "twenty oh eight was rough", "2008 was rough"},
		{"year two thousand", "two thousand twenty six", "2026"},
		{"informal hundreds left alone", "one seventy to one ninety base", "one seventy to one ninety base"},
		{"counting left alone", "two three four five", "two three four five"},
		{"or-less pair left alone", "four or five six years", "four or five six years"},
		{"vague hundred left alone", "a few hundred people", "a few hundred people"},
		{"article hundred left alone", "a hundred people", "a hundred people"},
		{"article hundred percent", "a hundred percent accurate", "100% accurate"},
		{"zero left alone", "starting from zero", "starting from zero"},
		{"one is a pronoun", "one thing at a time", "one thing at a time"},
		{"small count stays", "two years ago", "two years ago"},
		{"ten converts", "ten of them", "10 of them"},
		{"percent small", "five percent", "5%"},
		{"dollars small", "five dollars", "$5"},
		{"decimal", "three point five", "3.5"},
		{"decimal zero", "zero point five", "0.5"},
		{"million kept as word", "six million dollars", "$6 million"},
		{"decimal million", "two point five million", "2.5 million"},
		{"ordinal compound", "the twenty first century", "the 21st century"},
		{"ordinal small stays", "the second one", "the second one"},
		{"thirty second is a duration", "a thirty second delay", "a 30 second delay"},
		{"hundred and", "one hundred and five", "105"},
		{"sentence start", "Twenty people came.", "20 people came."},
		{"duration", "thirty minutes", "30 minutes"},
		{"single with period", "Fifteen.", "15."},
		{"whitespace kept", "ten  of", "10  of"},
		{"and between numbers", "two and three", "two and three"},
		{"point as a word", "the point is ten", "the point is 10"},
		{"big number grouped", "one hundred twenty five thousand", "125,000"},
		{"twenty one is 21", "twenty one", "21"},
		{"clause boundary ends run", "twenty, twenty", "20, 20"},
		{"suffix bound", "thirty-five-year-old", "thirty-five-year-old"},
		{"article billion stays", "like a billion contexts", "like a billion contexts"},
		{"article billion dollars", "a billion dollars", "$1 billion"},
		{"range takes one style", "maybe five to ten minutes", "maybe 5 to 10 minutes"},
		{"range or", "eight or twelve weeks", "8 or 12 weeks"},
		{"small range stays", "two or three weeks", "two or three weeks"},
		{"range both large", "ten to fifteen", "10 to 15"},
		{"range after clause stays", "one, to ten", "one, to 10"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConvertNumbers(tc.in); got != tc.want {
				t.Fatalf("ConvertNumbers(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStagesHonorContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, s := range []Stage{Fillers{}, Numbers{}} {
		if got, err := s.Apply(ctx, "um ten"); err == nil || got != "um ten" {
			t.Fatalf("%s on cancelled ctx = %q, %v", s.Name(), got, err)
		}
	}
	if got, err := New(Fillers{}, Numbers{}).Correct(context.Background(), "um, twenty five people"); err != nil || got != "25 people" {
		t.Fatalf("chain = %q, %v", got, err)
	}
}

func BenchmarkStages(b *testing.B) {
	chain := New(Fillers{}, Numbers{})
	text := strings.Repeat("um, so the meeting with, uh, the twenty five people ran thirty minutes over ", 4)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := chain.Correct(context.Background(), text); err != nil {
			b.Fatal(err)
		}
	}
}
