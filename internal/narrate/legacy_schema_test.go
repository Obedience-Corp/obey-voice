package narrate

import "testing"

// Plans written before the obey-voice rename declare the samantha schema tag
// and must keep validating so existing narration work is not orphaned.
func TestPlanValidateAcceptsLegacySchema(t *testing.T) {
	p := fullPlan()
	p.Schema = legacySchemaVersion
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate(legacy schema) error = %v", err)
	}
	p.Schema = "samantha.narration-plan.v2"
	if err := p.Validate(); err == nil {
		t.Fatal("Validate(unknown schema) error = nil, want rejection")
	}
}
