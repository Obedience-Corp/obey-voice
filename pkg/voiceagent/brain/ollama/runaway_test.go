package ollama

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestRunawayTextGuardAcrossDeltaBoundaries(t *testing.T) {
	for _, size := range []int{1, 17, 10000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			for _, tc := range []struct {
				text string
				loop bool
			}{
				{"Here is the status. camp" + strings.Repeat("und", 1000), true},
				{"A longer cycle " + strings.Repeat("abcdefghijklmnop", 40), true},
				{strings.Repeat("Repeated headings are ordinary prose. ", 50), false},
				{"A long identifier " + strings.Repeat("abcdefghijklmnopqrstuvwxyz", 30), false},
				{"A literal: `" + strings.Repeat("und", 500) + "`.", false},
				{"A literal containing ticks: `` `" + strings.Repeat("und", 500) + "` ``.", false},
				{"```text\n" + strings.Repeat("a", 1000) + "\n```\nDone.", false},
				{"日本語の文章はそのまま。 Normal words still stream.", false},
			} {
				var guard runawayTextGuard
				var output strings.Builder
				var err error
				for remaining := tc.text; remaining != "" && err == nil; {
					n := min(size, len(remaining))
					var text string
					text, err = guard.feed(remaining[:n])
					output.WriteString(text)
					remaining = remaining[n:]
				}
				output.WriteString(guard.flush())
				if tc.loop {
					if !errors.Is(err, ErrResponseRepetition) || output.Len() > 100 {
						t.Fatalf("runaway escaped: bytes=%d err=%v", output.Len(), err)
					}
				} else if err != nil || output.String() != tc.text {
					t.Fatalf("valid output changed: bytes=%d err=%v", output.Len(), err)
				}
			}
		})
	}
}
