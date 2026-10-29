package events

import "time"

// CompactionProgress reports working-context maintenance. Terminal stages
// (complete, skipped, canceled, failed) always clear the progress display.
type CompactionProgress struct {
	Stage   string
	Elapsed time.Duration
}

func (e CompactionProgress) eventType() string { return "compaction.progress" }
