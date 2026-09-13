package log

import (
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/listen"
)

// OnUtterance implements listen.Sink.
func (w *Writer) OnUtterance(u listen.Utterance) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	endMS := u.At.Sub(w.origin()).Milliseconds()
	if endMS < 0 {
		endMS = 0
	}
	startMS := endMS - estimatedSpeechDuration(u.Text).Milliseconds()
	if startMS < w.lastUtteranceEndMS {
		startMS = w.lastUtteranceEndMS
	}
	if startMS < 0 {
		startMS = 0
	}
	if endMS <= startMS {
		endMS = startMS + 1
	}
	record := TranscriptRecord{
		ID:      fmt.Sprintf("utterance-%d", w.utterances+1),
		StartMS: startMS,
		EndMS:   endMS,
		Text:    u.Text,
	}
	if err := w.writeLog(fmt.Sprintf("[%s] %s\n", u.At.Format("15:04:05"), u.Text)); err != nil {
		return err
	}
	if err := w.writeEventAt(Event{
		Type: TypeUtterance, ID: record.ID, Text: u.Text,
		TS: u.At.Format(time.RFC3339), StartMS: startMS, EndMS: endMS, Timing: "estimated",
	}, endMS); err != nil {
		return err
	}
	w.transcripts = append(w.transcripts, record)
	w.lastUtteranceEndMS = endMS
	w.utterances++
	return nil
}

func estimatedSpeechDuration(text string) time.Duration {
	words := len(strings.Fields(text))
	if words < 1 {
		words = 1
	}
	d := time.Duration(words) * 350 * time.Millisecond
	if d < 800*time.Millisecond {
		return 800 * time.Millisecond
	}
	if d > 15*time.Second {
		return 15 * time.Second
	}
	return d
}

// OnTimeout implements listen.Sink. Silence writes nothing.
func (w *Writer) OnTimeout() error { return nil }

// OnError implements listen.Sink: errors are part of the record.
func (w *Writer) OnError(err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if writeErr := w.writeLog(fmt.Sprintf("[%s] [transcription error: %v]\n", now.Format("15:04:05"), err)); writeErr != nil {
		return writeErr
	}
	if writeErr := w.writeEvent(Event{Type: TypeError, Message: err.Error(), TS: now.Format(time.RFC3339)}); writeErr != nil {
		return writeErr
	}
	w.errors++
	return nil
}

// AddNote records a typed note at the current offset (meeting-relative).
func (w *Writer) AddNote(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if err := w.writeLog(fmt.Sprintf("[%s] 📝 note: %s\n", now.Format("15:04:05"), text)); err != nil {
		return err
	}
	if err := w.writeEvent(Event{Type: TypeNote, Text: text, TS: now.Format(time.RFC3339)}); err != nil {
		return err
	}
	w.notes++
	return nil
}

// AddBookmark marks an important moment (Plaude-style). Label defaults to
// "important". Optional text is a short caption from the note field.
func (w *Writer) AddBookmark(label, text string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "important"
	}
	text = strings.TrimSpace(text)
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	line := fmt.Sprintf("[%s] ★ %s", now.Format("15:04:05"), strings.ToUpper(label))
	if text != "" {
		line += ": " + text
	}
	line += "\n"
	if err := w.writeLog(line); err != nil {
		return err
	}
	if err := w.writeEvent(Event{
		Type:  TypeBookmark,
		Label: label,
		Text:  text,
		TS:    now.Format(time.RFC3339),
	}); err != nil {
		return err
	}
	w.bookmarks++
	return nil
}
