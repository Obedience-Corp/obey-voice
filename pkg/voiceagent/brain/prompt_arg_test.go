package brain

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

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

// A request is measured in bytes on Linux and macOS: one of fewer runes than
// the limit but more bytes, in CJK or emoji, is refused, and the line it
// leaves in a fresh session's recent lines is shortened on a rune boundary.
func TestGrokOrCursorRequestOverTheLimitInBytesButNotRunesIsRefused(t *testing.T) {
	if promptArgLimit.unit != "bytes" {
		t.Skip("measured in UTF-16 units here")
	}
	for _, long := range []string{strings.Repeat("漢字", 20000), strings.Repeat("😀", 30000)} {
		if utf8.RuneCountInString(long) >= maxPromptArgBytes || len(long) <= maxPromptArgBytes {
			t.Fatalf("a line of %d runes and %d bytes does not test the limit", utf8.RuneCountInString(long), len(long))
		}
		for _, provider := range []string{providerGrok, providerCursor} {
			prompts, errs := argPrompts(t, provider, long, "Hello?")
			if !errors.Is(errs[0], errPromptArgTooLarge) || errs[1] != nil || len(prompts) != 1 {
				t.Fatalf("%s: errors %v, %d requests ran", provider, errs, len(prompts))
			}
			if !utf8.ValidString(prompts[0]) || len(prompts[0]) > maxPromptArgBytes || !strings.Contains(prompts[0], "more characters of this line omitted]") {
				t.Fatalf("%s: the refused line was not shortened on a rune boundary within the limit:\n%.300s", provider, prompts[0])
			}
		}
	}
}

func TestWindowsArgUnitsCountsTheEscapedCommandLine(t *testing.T) {
	for _, c := range []struct {
		arg   string
		units int
	}{
		{"", 3},                             // a space and ""
		{"abc", 4},                          // a space and abc
		{"a b", 6},                          // quoted
		{`say "hi"`, 13},                    // quoted, each quote escaped
		{`C:\a b\`, 11},                     // quoted, the trailing backslash doubled
		{`a\\"b`, 9},                        // unquoted, both backslashes before the quote doubled, the quote escaped
		{"漢字", 3},                           // six bytes, one unit each
		{"😀😀", 5},                           // eight bytes, a surrogate pair each
		{"\xff", 2},                         // an invalid byte is one unit
		{strings.Repeat("😀", 16000), 32001}, // under 100 KiB in bytes, over the Windows command line
		{"a " + strings.Repeat("漢", 14000), 14005}, // 42 KB, 14,000 units
	} {
		if got := windowsArgUnits(c.arg); got != c.units {
			t.Errorf("windowsArgUnits(%.20q) = %d, want %d", c.arg, got, c.units)
		}
	}
	if got := windowsArgRoom("abc", "a b"); got != windowsCommandLineUnits-4-6 {
		t.Errorf("room beside two arguments = %d", got)
	}
}

// On Windows a request and the CLI's other arguments share one command line,
// measured in UTF-16 units: a request that fits in bytes but not in units is
// refused, the other arguments leave it less room, and a catch-up record of
// quotes, which escaping doubles, still fits beside the rest.
func TestWindowsLimitBoundsTheWholeCommandLine(t *testing.T) {
	fixed := func(request string) func(int) string {
		return func(int) string { return request }
	}
	if _, err := windowsArgLimit.prompt("grok", fixed(strings.Repeat("😀", 16000))); !errors.Is(err, errPromptArgTooLarge) {
		t.Fatalf("64 KB of emoji, 32,001 units, was not refused: %v", err)
	}
	cjk := "a " + strings.Repeat("漢", 14000)
	if _, err := windowsArgLimit.prompt("grok", fixed(cjk)); err != nil {
		t.Fatalf("14,005 units were refused: %v", err)
	}
	system := strings.Repeat("s", 20000)
	if _, err := windowsArgLimit.prompt("grok", fixed(cjk), system); !errors.Is(err, errPromptArgTooLarge) {
		t.Fatalf("a request that fits only without a 20,000-unit system prompt was not refused: %v", err)
	}

	base := "Reply to this request. " + strings.Repeat("x", 4000)
	render := func(budget int) string { return base + strings.Repeat(`"`, max(budget, 0)) }
	prompt, err := windowsArgLimit.prompt("grok", render, system)
	if err != nil {
		t.Fatal(err)
	}
	if record := len(prompt) - len(base); record == 0 || windowsArgUnits(prompt) > windowsArgRoom(system) {
		t.Fatalf("record of %d bytes, %d units of %d", record, windowsArgUnits(prompt), windowsArgRoom(system))
	}
}
