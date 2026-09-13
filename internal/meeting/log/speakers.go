package log

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// WriteSpeakerAnalysis appends post-capture status, timeline, and attributed
// utterances. Historical transcript lines remain untouched; the additive
// section and event types make fallback/reprocessing explicit.
//
// Diarization is background enrichment and may finish after Close: when the
// writer is already closed, the bundle files are reopened in append mode for
// this write (the section and events are additive, and readers already
// tolerate post-session_end appends — see meeting.AppendRoutedEvent).
func (w *Writer) WriteSpeakerAnalysis(analysis SpeakerAnalysis) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		reopen, err := w.reopenForAppend()
		if err != nil {
			return err
		}
		defer reopen()
	}

	speakers := make(map[string]struct{})
	for _, segment := range analysis.Segments {
		if segment.Label != "" && segment.Label != "unknown" {
			speakers[segment.Label] = struct{}{}
		}
	}
	w.speakerAnalysis = analysis
	w.speakerCount = len(speakers)

	var section strings.Builder
	section.WriteString("\n# Speaker analysis: ")
	section.WriteString(analysis.Status)
	if len(speakers) > 0 {
		noun := "speakers"
		if len(speakers) == 1 {
			noun = "speaker"
		}
		fmt.Fprintf(&section, " (%d %s)", len(speakers), noun)
	}
	if analysis.Error != "" {
		section.WriteString(" — ")
		section.WriteString(analysis.Error)
	}
	section.WriteString("\n")
	if analysis.Artifact != "" {
		fmt.Fprintf(&section, "# Speaker analysis file: %s\n", analysis.Artifact)
	}
	if analysis.AudioFile != "" {
		fmt.Fprintf(&section, "# Meeting audio: %s\n", analysis.AudioFile)
	}
	if len(analysis.Segments) > 0 {
		section.WriteString("# Speaker timeline\n")
		for _, segment := range analysis.Segments {
			fmt.Fprintf(&section, "[%s] %s\n", formatOffsetRange(segment.StartMS, segment.EndMS), segment.Label)
		}
	}
	if len(analysis.Utterances) > 0 {
		section.WriteString("# Speaker-attributed transcript\n")
		for _, utterance := range analysis.Utterances {
			fmt.Fprintf(&section, "[%s] %s: %s\n",
				formatOffsetRange(utterance.StartMS, utterance.EndMS), utterance.Speaker, utterance.Text)
		}
	}
	if err := w.writeLog(section.String()); err != nil {
		return err
	}

	statusEvent := Event{
		Type: TypeSpeakerAnalysis, Status: analysis.Status, Message: analysis.Error,
		Artifact: analysis.Artifact, AudioFile: analysis.AudioFile, SpeakerCount: len(speakers),
	}
	if err := w.writeEvent(statusEvent); err != nil {
		return err
	}
	for _, segment := range analysis.Segments {
		if err := w.writeEvent(Event{
			Type: TypeSpeakerSegment, ID: segment.ID, Label: segment.Label,
			StartMS: segment.StartMS, EndMS: segment.EndMS,
			Confidence: segment.Confidence, State: segment.State,
			TS: w.started.Add(time.Duration(segment.StartMS) * time.Millisecond).Format(time.RFC3339),
		}); err != nil {
			return err
		}
	}
	// A resumed meeting re-analyzes the whole recording, so most of these
	// utterances are byte-identical to the ones the previous pass already
	// wrote. Emitting only what changed keeps the stream from growing a full
	// transcript per stop; readers still collapse by id (LatestByID) because
	// an older bundle has the duplicates.
	for _, utterance := range analysis.Utterances {
		if prior, ok := w.priorUtterances[utterance.ID]; ok && utterance.ID != "" && prior == utterance {
			continue
		}
		if err := w.writeEvent(Event{
			Type: TypeSpeakerUtterance, ID: utterance.ID, Text: utterance.Text,
			Label: utterance.Speaker, StartMS: utterance.StartMS, EndMS: utterance.EndMS,
			Confidence: utterance.Confidence, State: utterance.State, Timing: "estimated",
			TS: w.started.Add(time.Duration(utterance.EndMS) * time.Millisecond).Format(time.RFC3339),
		}); err != nil {
			return err
		}
		if utterance.ID != "" {
			if w.priorUtterances == nil {
				w.priorUtterances = make(map[string]SpeakerUtterance)
			}
			w.priorUtterances[utterance.ID] = utterance
		}
	}
	return nil
}

func formatOffsetRange(startMS, endMS int64) string {
	format := func(ms int64) string {
		if ms < 0 {
			ms = 0
		}
		d := time.Duration(ms) * time.Millisecond
		return fmt.Sprintf("%02d:%02d:%02d.%03d",
			int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, int(d.Milliseconds())%1000)
	}
	return format(startMS) + "–" + format(endMS)
}

// reopenForAppend swaps in append-mode file handles for a post-Close
// enrichment write. Caller must hold w.mu and defer the returned restore.
func (w *Writer) reopenForAppend() (restore func(), err error) {
	logF, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("meetinglog: reopen log for append: %w", err)
	}
	jsonlF, err := os.OpenFile(w.jsonlPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = logF.Close()
		return nil, fmt.Errorf("meetinglog: reopen jsonl for append: %w", err)
	}
	prevLog, prevJSONL := w.log, w.jsonl
	w.log, w.jsonl = logF, jsonlF
	return func() {
		_ = logF.Close()
		_ = jsonlF.Close()
		w.log, w.jsonl = prevLog, prevJSONL
	}, nil
}
