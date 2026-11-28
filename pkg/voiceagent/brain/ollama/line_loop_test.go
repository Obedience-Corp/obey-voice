package ollama

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestLineLoopGuard(t *testing.T) {
	picture := "Let me make you a picture."
	fence := "```\n" + strings.Repeat("fmt.Println(\"the same line of code\")\n", 5) + "```\nDone."
	for _, tc := range []struct {
		name, text, want string
		loop             bool
	}{
		{"the reported loop", strings.Repeat(picture+"\n\n", 40), picture + "\n\n", true},
		{"loop on one line", "Sure thing. " + strings.Repeat(picture+" ", 40), "Sure thing. " + picture + " ", true},
		{"loop after other text", "Okay.\n\n" + strings.Repeat(picture+"\n", 3) + "Here it is.", "Okay.\n\n" + picture + "\n", true},
		{"two copies are left alone", picture + "\n" + picture + "\nHere it is.", "", false},
		{"two copies at the end", picture + " " + picture, "", false},
		{"a sentence that only starts the same", picture + " " + picture + " Let me make you a picture of a fox.", "", false},
		{"short repeats", "No. No. No. No. Ho ho ho! Ho ho ho! Ho ho ho!", "", false},
		{"code block", fence, "", false},
		{"inline code", strings.Repeat("Run `go test ./...` once more.\n", 4), "", false},
		{"varied lines", "First line of the list.\nSecond line of the list.\nFirst line of the list.\n", "", false},
	} {
		for _, size := range []int{1, 7, 10000} {
			t.Run(tc.name+"/"+strconv.Itoa(size), func(t *testing.T) {
				var g lineLoopGuard
				var out strings.Builder
				var err error
				for rest := tc.text; rest != "" && err == nil; {
					n := min(size, len(rest))
					var sent string
					sent, err = g.feed(rest[:n])
					out.WriteString(sent)
					rest = rest[n:]
				}
				out.WriteString(g.flush())
				if tc.loop {
					if !errors.Is(err, ErrResponseRepetition) || out.String() != tc.want {
						t.Fatalf("sent %q err %v, want %q and a stop", out.String(), err, tc.want)
					}
					if _, again := g.feed("More text."); !errors.Is(again, ErrResponseRepetition) {
						t.Fatal("a stopped reply took more text")
					}
				} else if err != nil || out.String() != tc.text {
					t.Fatalf("sent %q err %v, want it unchanged", out.String(), err)
				}
			})
		}
	}
}

// A request to repeat something is the task, not a loop.
func TestRequestedRepetitionPassesTheLoopGuard(t *testing.T) {
	g := newRunawayTextGuard("Repeat this line five times: let me make you a picture")
	text := strings.Repeat("Let me make you a picture.\n", 5)
	sent, err := g.feed(text)
	if sent += g.flush(); err != nil || sent != text {
		t.Fatalf("sent %q err %v", sent, err)
	}
}
