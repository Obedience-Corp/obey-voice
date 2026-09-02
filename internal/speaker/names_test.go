package speaker

import "testing"

func TestNormalizeID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"1", "speaker-1"},
		{"speaker-2", "speaker-2"},
		{"Speaker-3", "speaker-3"},
		{"  12 ", "speaker-12"},
		{"", ""},
		{"0", ""},
		{"speaker-0", ""},
		{"speaker-", ""},
		{"alice", ""},
		{"speaker-1a", ""},
	}
	for _, tt := range tests {
		if got := NormalizeID(tt.in); got != tt.want {
			t.Errorf("NormalizeID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNameMapSetDisplayAndClear(t *testing.T) {
	m := NewNameMap()
	if err := m.Set("1", "Lance"); err != nil {
		t.Fatal(err)
	}
	if got := m.Display("speaker-1"); got != "Lance" {
		t.Fatalf("Display = %q, want Lance", got)
	}
	if got := m.Display("speaker-2"); got != "speaker-2" {
		t.Fatalf("unbound Display = %q, want speaker-2", got)
	}
	if err := m.Set("speaker-1", ""); err != nil {
		t.Fatal(err)
	}
	if got := m.Display("1"); got != "speaker-1" {
		t.Fatalf("cleared Display = %q, want speaker-1", got)
	}
}

func TestNameMapSeedPrimary(t *testing.T) {
	m := NewNameMap()
	m.SeedPrimary("  Lance  ")
	if got := m.Display("speaker-1"); got != "Lance" {
		t.Fatalf("seeded Display = %q, want Lance", got)
	}
	if got := m.Display("speaker-2"); got != "speaker-2" {
		t.Fatalf("multi-cluster must not guess speaker-2, got %q", got)
	}
	m.SeedPrimary("Alex")
	if got := m.Display("speaker-1"); got != "Lance" {
		t.Fatalf("second seed must not overwrite, got %q", got)
	}
	empty := NewNameMap()
	empty.SeedPrimary("")
	empty.SeedPrimary("   ")
	if snap := empty.Snapshot(); len(snap) != 0 {
		t.Fatalf("empty name must not bind, snapshot=%+v", snap)
	}
}

func TestNameMapSnapshotSorted(t *testing.T) {
	m := NewNameMap()
	_ = m.Set("3", "C")
	_ = m.Set("1", "A")
	_ = m.Set("2", "B")
	snap := m.Snapshot()
	if len(snap) != 3 || snap[0].ID != "speaker-1" || snap[2].Name != "C" {
		t.Fatalf("snapshot = %+v", snap)
	}
}
