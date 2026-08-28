package textclean

import "testing"

func TestStripThinkBlocks(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"no tags", "Hello speaker-2!", "Hello speaker-2!"},
		{"think then reply", "<think>The user said hi.</think>\nHello there!", "Hello there!"},
		{"thinking alias", "<thinking>plan the greeting</thinking>Hi.", "Hi."},
		{"unclosed eats the rest", "<think>still reasoning", ""},
		{"reply then think", "Hi.\n<think>aside</think>", "Hi."},
		{"I think is not a tag", "I think efficiency is overrated, honestly.", "I think efficiency is overrated, honestly."},
		{"inner comparison survives", "Is 3 < 4? Yes.", "Is 3 < 4? Yes."},
		{"case insensitive", "<THINK>secret</THINK>Spoken.", "Spoken."},
		{"mid-sentence", `She said <think>don't speak this</think> hello.`, "She said hello."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripThinkBlocks(tt.in); got != tt.want {
				t.Errorf("StripThinkBlocks(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitThink(t *testing.T) {
	think, body := SplitThink("<think>The user said hi.</think>\nHello speaker-2!")
	if think != "The user said hi." {
		t.Errorf("think = %q", think)
	}
	if body != "Hello speaker-2!" {
		t.Errorf("body = %q", body)
	}

	think, body = SplitThink("just a reply")
	if think != "" || body != "just a reply" {
		t.Errorf("no-tag split think=%q body=%q", think, body)
	}

	think, body = SplitThink("<think>still going")
	if think != "still going" || body != "" {
		t.Errorf("unclosed think=%q body=%q", think, body)
	}
}

func TestThinkFilterSpansSegments(t *testing.T) {
	var f ThinkFilter
	if got := f.Feed(`<think>The user says "speaker-2: Hi santa".`); got != "" {
		t.Fatalf("open segment = %q, want empty", got)
	}
	if !f.InThink() {
		t.Fatal("expected to be inside think after open tag")
	}
	if got := f.Feed("This is a greeting."); got != "" {
		t.Fatalf("think body = %q, want empty (must not be spoken)", got)
	}
	if got := f.Feed("</think>"); got != "" {
		t.Fatalf("close tag = %q, want empty", got)
	}
	if f.InThink() {
		t.Fatal("expected think region closed")
	}
	if got := f.Feed("Hello speaker-2! It's a wonderful day."); got != "Hello speaker-2! It's a wonderful day." {
		t.Fatalf("spoken = %q", got)
	}
}

func TestThinkFilterHoldsIncompleteTag(t *testing.T) {
	var f ThinkFilter
	if got := f.Feed("Hi <thi"); got != "Hi " {
		t.Fatalf("partial open = %q, want the prefix kept", got)
	}
	if got := f.Feed("nk>secret</think> there."); got != " there." {
		t.Fatalf("after completing the tag = %q, want the spoken tail", got)
	}
}

func TestThinkFilterDoesNotEatComparisons(t *testing.T) {
	var f ThinkFilter
	in := "Is 3 < 4? Yes."
	if got := f.Feed(in); got != in {
		t.Fatalf("got %q, want the comparison intact", got)
	}
}

func TestPrepareSpokenTextStripsThinkBlocks(t *testing.T) {
	in := "<think>The user says hi. Plan a greeting.</think>Hello speaker-2!"
	if got := PrepareSpokenText(in); got != "Hello speaker-2!" {
		t.Fatalf("PrepareSpokenText = %q, want the spoken reply only", got)
	}
}
