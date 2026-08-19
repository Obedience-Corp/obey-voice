package audiobook

import "testing"

// Audiobook plans written before the obey-voice rename declare the samantha
// schema tag; --resume must keep accepting them.
func TestPlanValidateAcceptsLegacySchema(t *testing.T) {
	p := &Plan{
		Schema: legacySchemaVersion, Source: Source{Path: "book.epub", Format: "epub"},
		Defaults: Defaults{Decision: DecisionReview},
		Sections: []Section{
			{ID: "one", Order: 1, Kind: "main_content", Suggestion: DecisionInclude, Decision: DecisionInclude, ExtractedPath: "extracted/one.txt"},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate(legacy schema) error = %v", err)
	}
	p.Schema = "samantha.audiobook-plan.v2"
	if err := p.Validate(); err == nil {
		t.Fatal("Validate(unknown schema) error = nil, want rejection")
	}
}
