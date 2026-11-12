package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func noBackupCoalescing(t *testing.T) {
	t.Helper()
	prev := backupCoalesce
	backupCoalesce = 0
	t.Cleanup(func() { backupCoalesce = prev })
}

func TestSetKeyFileValuesIsOneSave(t *testing.T) {
	path := newInstall(t, commentedConfig)

	err := SetKeyFileValues([]KeyWrite{
		{Key: "vad_silence_duration", Value: 0.9},
		{Key: "agent_name", Value: "Ada"},
		{Key: "persona", Value: "ada"},
	})
	if err != nil {
		t.Fatalf("SetKeyFileValues: %v", err)
	}
	if backups := backupsFor(t, path); len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one for a three-key save", backups)
	}
	for key, want := range map[string]string{"agent_name": "Ada", "persona": "ada"} {
		if got, _ := Get(key).(string); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestSetKeyFileValuesRefusesWholeBatchOnBadKey(t *testing.T) {
	path := newInstall(t, commentedConfig)
	before, _ := os.ReadFile(path)

	err := SetKeyFileValues([]KeyWrite{
		{Key: "agent_name", Value: "Ada"},
		{Key: "no_such_key", Value: "x"},
	})
	if err == nil {
		t.Fatal("want an error for an unknown key")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a failed batch changed the file")
	}
	if backups := backupsFor(t, path); len(backups) != 0 {
		t.Errorf("backups = %v, want none", backups)
	}
}

func TestIdenticalSaveWritesNothing(t *testing.T) {
	path := newInstall(t, commentedConfig)
	if err := SetKeyFileValues([]KeyWrite{{Key: "agent_name", Value: "Ada"}}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	before, _ := os.Stat(path)
	backups := len(backupsFor(t, path))

	if err := SetKeyFileValues([]KeyWrite{{Key: "agent_name", Value: "Ada"}}); err != nil {
		t.Fatalf("repeat save: %v", err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an identical save rewrote the file")
	}
	if got := len(backupsFor(t, path)); got != backups {
		t.Errorf("backups %d -> %d, want no new backup", backups, got)
	}
}

func TestBackupAndReplaceSkipsIdenticalContent(t *testing.T) {
	path := newInstall(t, commentedConfig)
	data, _ := os.ReadFile(path)

	backup, err := backupAndReplace(path, data, true, "k")
	if err != nil {
		t.Fatalf("backupAndReplace: %v", err)
	}
	if backup != "" || len(backupsFor(t, path)) != 0 {
		t.Errorf("identical replace made a backup: %q %v", backup, backupsFor(t, path))
	}
}

func TestBurstOfSavesKeepsOlderBackup(t *testing.T) {
	path := newInstall(t, commentedConfig)
	old := path + ".bak." + time.Now().UTC().Add(-48*time.Hour).Format("20060102T150405.000000000Z")
	if err := os.WriteFile(old, []byte("agent_name: Original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, v := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		if err := SetKeyFileValues([]KeyWrite{{Key: "agent_name", Value: v}}); err != nil {
			t.Fatalf("save %s: %v", v, err)
		}
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("older distinct backup was rotated away by a burst: %v", err)
	}
	if got := len(backupsFor(t, path)); got != 2 {
		t.Errorf("backups = %d, want the old one plus one for the burst", got)
	}
}

func TestBackupSkippedWhenNewestMatchesCurrent(t *testing.T) {
	path := newInstall(t, commentedConfig)
	noBackupCoalescing(t)
	current, _ := os.ReadFile(path)
	same := path + ".bak." + time.Now().UTC().Add(-time.Hour).Format("20060102T150405.000000000Z")
	if err := os.WriteFile(same, current, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyFileValues([]KeyWrite{{Key: "agent_name", Value: "Ada"}}); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(path + ".bak.*")
	if len(matches) != 1 {
		t.Errorf("backups = %v, want only the pre-existing identical one", matches)
	}
}
