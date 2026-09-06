package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// eventLog writes every bus event to events.jsonl as it happens, so a run
// that dies mid-turn still leaves the sequence on disk.
type eventLog struct {
	mu    sync.Mutex
	f     *os.File
	enc   *json.Encoder
	start time.Time
}

type eventLine struct {
	At    string  `json:"at"`
	T     float64 `json:"t_s"`
	Type  string  `json:"type"`
	Event any     `json:"event,omitempty"`
	Err   string  `json:"error,omitempty"`
}

func openEventLog(path string) (*eventLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &eventLog{f: f, enc: json.NewEncoder(f), start: time.Now()}, nil
}

func (l *eventLog) add(e events.Event) {
	now := time.Now()
	line := eventLine{
		At:    now.UTC().Format(time.RFC3339Nano),
		T:     now.Sub(l.start).Seconds(),
		Type:  strings.TrimPrefix(fmt.Sprintf("%T", e), "events."),
		Event: e,
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.enc.Encode(line); err != nil {
		line.Event = nil
		line.Err = err.Error()
		_ = l.enc.Encode(line)
	}
}

func (l *eventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
