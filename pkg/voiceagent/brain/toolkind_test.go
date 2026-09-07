package brain

import "testing"

func TestKindFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want ToolKind
	}{
		{"", KindOther},
		{"nonesuch", KindOther},
		{"Read_File", KindOther}, // names are wire-exact, not case folded
		{"read_file", KindRead},
		{"list_files", KindListDir},
		{"write_file", KindWrite},
		{"run_command", KindExecute},
		{"web_search", KindWebSearch},
		{"fetch_url", KindWebFetch},
		{"read_skill", KindSkill},
		{"activate_skill", KindSkill},
		{"agent_delegate", KindTask},
		{"agent_cancel", KindTask},
		{"agent_delegations", KindTask},
		{"route", KindRoute},
	}
	for _, tt := range tests {
		if got := KindFor(tt.name); got != tt.want {
			t.Errorf("KindFor(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
