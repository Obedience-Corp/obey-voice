package main

import (
	"context"
	"encoding/json"
	"errors"
	engine "github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd"
	"testing"
	"time"
)

func TestPersonaPreviewWithoutConversationReplacementStopAndError(t *testing.T) {
	writer := &eventWriter{messages: make(chan map[string]any, 20)}
	out := &output{enc: json.NewEncoder(writer)}
	started := make(chan engine.DesktopVoiceSelection, 4)
	drained := make(chan struct{}, 4)
	h := &host{previewVoice: func(ctx context.Context, selection engine.DesktopVoiceSelection) error {
		started <- selection
		if selection.Voice == "broken" {
			return errors.New("synthesis unavailable")
		}
		<-ctx.Done()
		drained <- struct{}{}
		return ctx.Err()
	}}
	defer h.close(out)
	draft := engine.DesktopVoiceSelection{Provider: "qwen3-tts", Voice: "Ryan", Tier: "1.7b"}
	start := func(id uint64, selection engine.DesktopVoiceSelection) {
		t.Helper()
		if _, err := h.handle(t.Context(), request{ID: id, Method: "persona_preview", VoiceSelection: selection}, out); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-started:
			if got != selection {
				t.Fatalf("selection = %+v", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("preview did not start")
		}
	}
	start(1, draft)
	if h.controller != nil {
		t.Fatal("preview started a conversation")
	}
	if got, err := h.handle(t.Context(), request{Method: "ping"}, out); err != nil || got != "pong" {
		t.Fatal("bridge blocked during preview")
	}
	draft.Voice = "Uncle_Fu"
	start(2, draft)
	select {
	case <-drained:
	default:
		t.Fatal("replacement overlapped previous preview")
	}
	if _, err := h.handle(t.Context(), request{Method: "persona_preview_stop"}, out); err != nil {
		t.Fatal(err)
	}
	writer.until(t, func(v map[string]any) bool {
		d, _ := v["data"].(map[string]any)
		return v["event"] == "persona_preview_done" && d["id"] == float64(2) && d["message"] == "Preview stopped"
	})
	draft.Voice = "broken"
	start(3, draft)
	writer.until(t, func(v map[string]any) bool {
		d, _ := v["data"].(map[string]any)
		return v["event"] == "persona_preview_done" && d["id"] == float64(3) && d["error"] == true && d["message"] == "synthesis unavailable"
	})
}

func TestPersonaPreviewShutdownDrains(t *testing.T) {
	writer := &eventWriter{messages: make(chan map[string]any, 5)}
	out := &output{enc: json.NewEncoder(writer)}
	started, drained := make(chan struct{}), make(chan struct{})
	h := &host{previewVoice: func(ctx context.Context, _ engine.DesktopVoiceSelection) error {
		close(started)
		<-ctx.Done()
		close(drained)
		return ctx.Err()
	}}
	h.startPreview(t.Context(), request{ID: 1}, out)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	h.close(out)
	select {
	case <-drained:
	default:
		t.Fatal("shutdown returned before preview cleanup")
	}
}
