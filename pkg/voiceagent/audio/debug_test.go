package audio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordDebugSynthesisIncludesOriginalAndPreparedText(t *testing.T) {
	root := t.TempDir()
	if err := SetDebugAudioDir(root); err != nil {
		t.Fatalf("SetDebugAudioDir() error = %v", err)
	}
	t.Cleanup(func() { _ = SetDebugAudioDir("") })

	RecordDebugSynthesis("kokoro", "It wasn't.", "It wasn't.")
	encoded, err := os.ReadFile(filepath.Join(root, "syntheses.jsonl"))
	if err != nil {
		t.Fatalf("read syntheses: %v", err)
	}
	var event debugSynthesisMetadata
	if err := json.Unmarshal(encoded, &event); err != nil {
		t.Fatalf("decode synthesis metadata: %v", err)
	}
	if event.Provider != "kokoro" || event.Original != "It wasn't." || event.Prepared != "It wasn't." {
		t.Fatalf("synthesis event = %+v", event)
	}
	if time.Since(event.CreatedAt) > time.Minute {
		t.Fatalf("synthesis timestamp is stale: %v", event.CreatedAt)
	}
}
