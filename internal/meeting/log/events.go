package log

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ReadEvents loads the JSONL event stream. Unknown types are kept so older
// readers and newer control events share one parser.
//
// A final line with no terminating newline is a torn write from a recording
// that died mid-append. It is dropped instead of failing the whole read, so an
// interrupted bundle still lists and still resumes; a malformed line anywhere
// else is a real corruption and still errors.
func ReadEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("meetinglog: open events %s: %w", path, err)
	}
	defer f.Close()
	terminated, err := endsWithNewline(f)
	if err != nil {
		return nil, fmt.Errorf("meetinglog: read events %s: %w", path, err)
	}

	var events []Event
	appendLine := func(line string, tolerate bool) error {
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if tolerate {
				return nil
			}
			return fmt.Errorf("meetinglog: parse events %s: %w", path, err)
		}
		events = append(events, e)
		return nil
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	// One line of lookahead: only the last line may be torn, and that is not
	// known until the scan ends.
	pending, havePending := "", false
	for sc.Scan() {
		if havePending {
			if err := appendLine(pending, false); err != nil {
				return nil, err
			}
		}
		pending, havePending = sc.Text(), true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("meetinglog: read events %s: %w", path, err)
	}
	if havePending {
		if err := appendLine(pending, !terminated); err != nil {
			return nil, err
		}
	}
	return events, nil
}

// trimTornTail truncates a JSONL file back to its last complete line. A file
// that already ends in a newline is left alone.
func trimTornTail(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("meetinglog: open events %s: %w", path, err)
	}
	defer f.Close()
	terminated, err := endsWithNewline(f)
	if err != nil {
		return fmt.Errorf("meetinglog: read events %s: %w", path, err)
	}
	if terminated {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("meetinglog: stat events %s: %w", path, err)
	}
	end := info.Size()
	const window = 64 * 1024
	buf := make([]byte, window)
	for end > 0 {
		start := end - window
		if start < 0 {
			start = 0
		}
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && n == 0 {
			return fmt.Errorf("meetinglog: read events %s: %w", path, err)
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				if err := f.Truncate(start + int64(i) + 1); err != nil {
					return fmt.Errorf("meetinglog: trim events %s: %w", path, err)
				}
				return nil
			}
		}
		end = start
	}
	// No newline anywhere: the whole file is one torn line.
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("meetinglog: trim events %s: %w", path, err)
	}
	return nil
}

// endsWithNewline reports whether the last byte closes the final line. ReadAt
// leaves the file offset alone, so the caller's scan still starts at byte 0.
func endsWithNewline(f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() == 0 {
		return true, nil
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], info.Size()-1); err != nil {
		return false, err
	}
	return b[0] == '\n', nil
}

// transcriptKey identifies one transcript line across passes. The utterance id
// is the real key; text at an offset covers hand-written fixtures and any
// stream whose events predate ids.
func transcriptKey(e Event) string {
	if e.ID != "" {
		return e.ID
	}
	return fmt.Sprintf("\x00%d|%s", e.OffsetMs, e.Text)
}

// MergeAttributedTranscript overlays speaker attribution onto the plain
// transcript rather than replacing it. Every utterance keeps its place and its
// text; a line the latest analysis pass labelled is swapped for that
// speaker_utterance, and a line no pass reached stays a plain utterance.
//
// Replacing wholesale loses real content. A resumed segment whose diarization
// was skipped, cancelled, or failed has no speaker_utterance of its own, and
// the earlier pass's events do not cover it, so the whole segment would vanish
// from the routed note and the review while the header still counted it.
//
// Each pass of a resumed meeting re-emits the transcript it analyzed, so the
// last event for a line is the current labelling.
func MergeAttributedTranscript(utterances, attributed []Event) []Event {
	if len(attributed) == 0 {
		return utterances
	}
	latest := make(map[string]Event, len(attributed))
	order := make([]string, 0, len(attributed))
	for _, e := range attributed {
		key := transcriptKey(e)
		if _, seen := latest[key]; !seen {
			order = append(order, key)
		}
		latest[key] = e
	}
	merged := make([]Event, 0, len(utterances)+len(attributed))
	placed := make(map[string]struct{}, len(latest))
	for _, e := range utterances {
		key := transcriptKey(e)
		if a, ok := latest[key]; ok {
			merged = append(merged, a)
			placed[key] = struct{}{}
			continue
		}
		merged = append(merged, e)
	}
	// An attributed line with no plain utterance behind it still belongs in
	// the transcript; dropping it would lose the only copy of that text.
	for _, key := range order {
		if _, done := placed[key]; done {
			continue
		}
		merged = append(merged, latest[key])
	}
	return merged
}
