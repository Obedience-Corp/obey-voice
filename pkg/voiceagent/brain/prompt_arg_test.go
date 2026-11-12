package brain

import (
	"errors"
	"strings"
	"testing"

	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
)

// argPrompts runs requests on a fresh Grok or Cursor session with no host
// catch-up and returns the prompts that reached the CLI and each stream error.
func argPrompts(t *testing.T, provider string, inputs ...string) ([]string, []error) {
	t.Helper()
	var errs []error
	switch provider {
	case providerGrok:
		fake := &fakeGrokClient{}
		for range inputs {
			fake.streamScripts = append(fake.streamScripts, grokStreamScript{events: []grok.Event{grokTextEvent("Noted.", "")}})
		}
		g := newTestGrokBrain(fake)
		for _, input := range inputs {
			_, res := collectStream(t, mustGrokStream(t, g, input))
			errs = append(errs, res.Err)
		}
		return fake.streamPrompts, errs
	default:
		fake := &fakeCursorClient{}
		c := newTestCursorBrain(fake)
		for _, input := range inputs {
			_, res := collectStream(t, mustCursorStream(t, c, input))
			errs = append(errs, res.Err)
		}
		return fake.prompts, errs
	}
}

// A line too large for one argument is refused without running the CLI, and
// does not wedge the requests after it: a fresh session's recent lines are
// held to what fits.
func TestGrokOrCursorRefusedLineDoesNotWedgeAFreshSession(t *testing.T) {
	for _, provider := range []string{providerGrok, providerCursor} {
		t.Run(provider, func(t *testing.T) {
			long := "PASTE-START " + strings.Repeat("amber ", 20000)
			prompts, errs := argPrompts(t, provider, long, "Hello?", "Still there?")
			if !errors.Is(errs[0], errPromptArgTooLarge) {
				t.Fatalf("first error = %v", errs[0])
			}
			if errs[1] != nil || errs[2] != nil {
				t.Fatalf("requests after the refused one failed: %v", errs)
			}
			if len(prompts) != 2 {
				t.Fatalf("the CLI ran %d times, want 2", len(prompts))
			}
			for _, prompt := range prompts {
				if len(prompt) > maxPromptArgBytes {
					t.Fatalf("prompt of %d bytes passed the CLI", len(prompt))
				}
				if !strings.Contains(prompt, "Recent conversation:") || !strings.Contains(prompt, "User: PASTE-START") || !strings.Contains(prompt, "more characters of this line omitted]") {
					t.Fatalf("the refused line is not in the recent lines, shortened:\n%.400s", prompt)
				}
			}
		})
	}
}
