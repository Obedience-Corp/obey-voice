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
		{"trailing bracket is a comparison and survives", "The threshold is <", "The threshold is <"},
		{"tag the model never finished is dropped", "Hi <th", "Hi"},
		{"stray close mid-string is dropped, not spoken", "All done </think> here.", "All done here."},
		{"stray close at the end is dropped", "All done here.</think>", "All done here."},
		{"a nested block leaks no literal tag", "<think>a<think>b</think>c</think>d", "cd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripThinkBlocks(tt.in); got != tt.want {
				t.Errorf("StripThinkBlocks(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A stray open tag in prose swallows the rest of the reply, and that is the
// deliberate half of the no-nesting rule at the top of think.go: with no depth
// counter an open must mean "reasoning from here", or a real block whose close
// has not streamed yet would be spoken aloud. The cost is a reply that talks
// *about* the tag, which these models do not write and a listener would rather
// lose than hear the reasoning of. Asserted here so the trade is a decision on
// the record instead of a surprise found in a transcript.
func TestStripThinkBlocksStrayOpenTagTruncates(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"stray open eats the tail", "All done <think> here.", "All done"},
		{"prose about the tag loses it", "Use the <think> tag for reasoning.", "Use the"},
		{"alias behaves the same", "A <thinking> block is XML.", "A"},
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

// Feed holds a trailing tag prefix hoping the next segment completes it. At end
// of stream nothing else can, so Flush is the only thing that can release it —
// a caller that never calls it drops that text silently.
func TestThinkFilterFlushReleasesHeldTail(t *testing.T) {
	tests := []struct {
		name, feed, wantFeed, wantFlush string
	}{
		{"open tag the model never finished is dropped", "Hi <th", "Hi ", ""},
		{"close tag the model never finished is dropped", "<think>reasoning</thin", "", ""},
		{"held text inside a think region is never released", "<think>reasoning <", "", ""},
		{"lone bracket is a comparison, not a tag", "Reply that trails off <", "Reply that trails off ", "<"},
		{"nothing held", "Plain reply.", "Plain reply.", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f ThinkFilter
			if got := f.Feed(tt.feed); got != tt.wantFeed {
				t.Fatalf("Feed(%q) = %q, want %q", tt.feed, got, tt.wantFeed)
			}
			if got := f.Flush(); got != tt.wantFlush {
				t.Fatalf("Flush() after %q = %q, want %q", tt.feed, got, tt.wantFlush)
			}
			if got := f.Flush(); got != "" {
				t.Fatalf("second Flush() = %q, want the hold consumed", got)
			}
		})
	}
}

// Both halves empty means the reply is nothing but think markup so far. The
// renderer keys the dim "thinking" state off exactly this, so it must not be
// confused with a reply that has no markup at all.
func TestSplitThinkEmptyBlockYieldsNothingToPaint(t *testing.T) {
	for _, in := range []string{"<think></think>", "<think>\n</think>", "<think> </think>", "<think>"} {
		think, body := SplitThink(in)
		if think != "" || body != "" {
			t.Errorf("SplitThink(%q) = (%q, %q), want both empty", in, think, body)
		}
	}
}

// ContentOffset is for callers that forward the model's raw bytes — the TUI
// paints the reasoning live — while still needing to know which bytes are the
// reply. State advances exactly as Feed's does, including across chunks.
func TestThinkFilterContentOffset(t *testing.T) {
	tests := []struct {
		name  string
		feeds []string
		want  []int
	}{
		{"no markup starts at once", []string{"Hello there."}, []int{0}},
		{"whole chunk is reasoning", []string{"<think>plan it"}, []int{14}},
		{"reply begins after the close tag", []string{"<think>plan it", "</think>Hi."}, []int{14, 8}},
		{"block and reply in one chunk", []string{"<think>plan it</think>Hi."}, []int{22}},
		{"open tag split across chunks", []string{"<thi", "nk>plan it</think>Hi."}, []int{4, 18}},
		{"held bracket that was a comparison clamps to zero", []string{"Is 3 <", "4? Yes."}, []int{0, 0}},
		{"empty chunk has no content", []string{""}, []int{0}},
		{"whitespace before the block is not the reply", []string{"\n<think>plan</think>\nObey: Hi."}, []int{21}},
		{"whitespace-only chunk is not the reply", []string{"\n", "<think>plan</think>", "\nObey: Hi."}, []int{1, 19, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f ThinkFilter
			for i, in := range tt.feeds {
				if got := f.ContentOffset(in); got != tt.want[i] {
					t.Fatalf("ContentOffset(%q) [feed %d] = %d, want %d", in, i, got, tt.want[i])
				}
			}
		})
	}
}

// ContentOffset must advance the filter the same way Feed does, or a caller
// mixing them would see the two disagree about where a region ends.
func TestThinkFilterContentOffsetAdvancesLikeFeed(t *testing.T) {
	chunks := []string{"<think>plan", " it", "</think>Hi ", "there."}
	var byOffset, byFeed ThinkFilter
	for _, c := range chunks {
		byOffset.ContentOffset(c)
		byFeed.Feed(c)
		if byOffset.InThink() != byFeed.InThink() {
			t.Fatalf("after %q: ContentOffset inThink=%v, Feed inThink=%v", c, byOffset.InThink(), byFeed.InThink())
		}
	}
}

// A frame landing mid-tag must paint nothing rather than the bytes so far —
// the same hold the speech path applies, so bubble and audio never disagree
// about what counts as a tag.
func TestSplitThinkHoldsPartialTags(t *testing.T) {
	tests := []struct {
		in, think, body string
	}{
		{"Hello <thi", "", "Hello"},
		{"<think>reasoning</thin", "reasoning", ""},
		{"<think>reasoning</think>Hi <t", "reasoning", "Hi"},
		// Only tag bytes go: the gap the tag left is the model's own spacing.
		// StripThinkBlocks repairs that because its output is spoken; this is
		// rendered, and the invariant the TUI leans on is that nothing but tags
		// is removed.
		{"stray </think> in prose", "", "stray  in prose"},
		{"Is 3 < 4? Yes.", "", "Is 3 < 4? Yes."},
	}
	for _, tt := range tests {
		think, body := SplitThink(tt.in)
		if think != tt.think || body != tt.body {
			t.Errorf("SplitThink(%q) = (%q, %q), want (%q, %q)", tt.in, think, body, tt.think, tt.body)
		}
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
