package discussion

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

func (a Audience) Validate(participants []Participant) error {
	known := map[string]bool{}
	for _, p := range participants {
		known[p.Key] = true
	}
	seen := map[string]bool{}
	for _, k := range a.Members {
		if !known[k] || seen[k] {
			return fmt.Errorf("unknown or duplicate audience key %q", k)
		}
		seen[k] = true
	}
	if len(a.Unresolved) > MaxMembers {
		return fmt.Errorf("too many unresolved audience names")
	}
	switch a.Kind {
	case "current":
		if len(a.Members) != 0 || a.Coverage != "none" || len(a.Unresolved) != 0 || a.Reason != "none" {
			return fmt.Errorf("invalid current audience")
		}
	case "direct", "group":
		if len(a.Members) == 0 || len(a.Unresolved) != 0 || (a.Reason != "explicit" && a.Reason != "contextual") {
			return fmt.Errorf("invalid addressed audience")
		}
		if a.Kind == "direct" && (len(a.Members) != 1 || a.Coverage != "none") {
			return fmt.Errorf("direct audience requires one member")
		}
		if a.Kind == "group" && (a.Coverage != "all" && a.Coverage != "relevant") {
			return fmt.Errorf("invalid group coverage")
		}
	case "clarify":
		if len(a.Members) != 0 || a.Coverage != "none" || (a.Reason != "unknown" && a.Reason != "ambiguous") {
			return fmt.Errorf("invalid clarification")
		}
	default:
		return fmt.Errorf("invalid audience kind %q", a.Kind)
	}
	return nil
}
func (n Next) Validate(c Context) error {
	if n.Kind == "yield" && n.Member == "" && n.Reason == "no_contribution" {
		return nil
	}
	if n.Kind != "select" || !slices.Contains(c.Eligible, n.Member) || !slices.Contains([]string{"new_point", "answer", "correction", "challenge", "revision"}, n.Reason) {
		return fmt.Errorf("invalid next participant decision")
	}
	return nil
}
func (p Contribution) Validate(c Context, key string, returning bool) error {
	if p.Kind == "pass" && p.Reason == "no_contribution" && p.Intent == "" && len(p.References) == 0 {
		return nil
	}
	if p.Kind != "speak" || strings.TrimSpace(p.Intent) == "" || !utf8.ValidString(p.Intent) || utf8.RuneCountInString(p.Intent) > 240 || len(p.References) > 2 {
		return fmt.Errorf("invalid participation decision (kind %s, intent characters %d, references %d)", p.Kind, utf8.RuneCountInString(p.Intent), len(p.References))
	}
	grounded := slices.Contains([]string{"answer", "correction", "challenge", "revision"}, p.Reason)
	if !grounded && !slices.Contains([]string{"perspective", "clarify", "agreement"}, p.Reason) {
		return fmt.Errorf("invalid contribution reason %q for %s", p.Reason, p.Kind)
	}
	if returning && !grounded {
		return fmt.Errorf("returning participant needs a grounded answer or revision")
	}
	if (returning || grounded) && len(p.References) == 0 {
		return fmt.Errorf("contribution requires a delivered response reference")
	}
	seen := map[string]bool{}
	for _, ref := range p.References {
		found := false
		for _, m := range c.Delivered {
			if m.ResponseID == ref {
				found = true
				break
			}
		}
		if !found || seen[ref] {
			return fmt.Errorf("invalid delivered reference %q", ref)
		}
		seen[ref] = true
	}
	return nil
}
