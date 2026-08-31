package netapi

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// TestEncodeEventPayloadsRoundTrip covers the two events that used to reach
// clients as a bare {"type":…} (OV0005 E1). Degenerate payloads come first:
// the encoder must carry them verbatim rather than drop the key, because a
// client distinguishes "field absent" (old serve) from "field empty".
func TestEncodeEventPayloadsRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		event events.Event
		want  map[string]any
	}{
		{
			name:  "response_delta empty text still carries the key",
			event: events.ResponseDelta{},
			want:  map[string]any{"type": "response_delta", "text": ""},
		},
		{
			name:  "thinking_delta empty text still carries the key",
			event: events.ThinkingDelta{},
			want:  map[string]any{"type": "thinking_delta", "text": ""},
		},
		{
			name:  "thinking_delta text survives",
			event: events.ThinkingDelta{Text: "The user said hi."},
			want:  map[string]any{"type": "thinking_delta", "text": "The user said hi."},
		},
		{
			name:  "audio_level zero level still carries the key",
			event: events.AudioLevel{Source: "input"},
			want:  map[string]any{"type": "audio_level", "source": "input", "level": float64(0)},
		},
		{
			name:  "audio_level unknown source is passed through, not defaulted",
			event: events.AudioLevel{Source: "", Level: 0.5},
			want:  map[string]any{"type": "audio_level", "source": "", "level": 0.5},
		},
		{
			name:  "response_delta multi-byte text survives",
			event: events.ResponseDelta{Text: "héllo — 世界"},
			want:  map[string]any{"type": "response_delta", "text": "héllo — 世界"},
		},
		{
			name:  "audio_level input level",
			event: events.AudioLevel{Source: "input", Level: 0.42},
			want:  map[string]any{"type": "audio_level", "source": "input", "level": 0.42},
		},
		{
			name:  "audio_level output level",
			event: events.AudioLevel{Source: "output", Level: 1},
			want:  map[string]any{"type": "audio_level", "source": "output", "level": float64(1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := marshalEvent(tt.event)
			if err != nil {
				t.Fatalf("marshalEvent: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", data, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("envelope = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("field %q = %v (%T), want %v (%T)", k, got[k], got[k], v, v)
				}
			}
		})
	}
}

// TestEncodeEventLeavesOtherShapesAlone guards the E1 blast radius: adding two
// cases to a closed switch must not change any other event's envelope.
func TestEncodeEventLeavesOtherShapesAlone(t *testing.T) {
	tests := []struct {
		event events.Event
		want  []string
	}{
		{events.ThinkingStarted{}, []string{"type"}},
		{events.ConversationCleared{}, []string{"type"}},
		{events.TranscriptPartial{Text: "part"}, []string{"type", "text"}},
		{events.ResponseStreamingStarted{}, []string{"type", "elapsed_ms"}},
		{events.SpeakingStarted{Text: "hi"}, []string{"type", "text"}},
		{events.TurnInterrupted{Reason: "remote_interrupt"}, []string{"type", "reason"}},
		{events.SpeakingInterrupted{Reason: "remote_interrupt"}, []string{"type", "reason"}},
	}

	for _, tt := range tests {
		env := encodeEvent(tt.event)
		if len(env) != len(tt.want) {
			t.Errorf("%T: envelope = %v, want keys %v", tt.event, env, tt.want)
			continue
		}
		for _, k := range tt.want {
			if _, ok := env[k]; !ok {
				t.Errorf("%T: envelope %v missing key %q", tt.event, env, k)
			}
		}
	}
}

// wireFixture is one live capture under testdata/wire: server→client frames,
// verbatim bytes, one per line, in arrival order.
func wireFixture(t *testing.T, name string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "wire", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var frames []map[string]any
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var env map[string]any
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("%s line %d is not a JSON envelope: %v", name, i+1, err)
		}
		if _, ok := env["type"].(string); !ok {
			t.Fatalf("%s line %d has no type discriminator: %s", name, i+1, line)
		}
		frames = append(frames, env)
	}
	return frames
}

// TestWireFixturesCarryPayloads is the E1 regression a unit test cannot give:
// these frames came off a real socket, so a payload that is dropped between
// the bus and the wire fails here even when encodeEvent looks right.
func TestWireFixturesCarryPayloads(t *testing.T) {
	tests := []struct {
		fixture   string
		eventType string
		fields    []string
	}{
		{"text_turn.jsonl", "response_delta", []string{"text"}},
		{"voice_turn.jsonl", "audio_level", []string{"source", "level"}},
	}

	for _, tt := range tests {
		t.Run(tt.fixture+"/"+tt.eventType, func(t *testing.T) {
			seen := 0
			for _, env := range wireFixture(t, tt.fixture) {
				if env["type"] != tt.eventType {
					continue
				}
				seen++
				if len(env) != len(tt.fields)+1 {
					t.Errorf("%s frame = %v, want type plus %v", tt.eventType, env, tt.fields)
				}
				for _, f := range tt.fields {
					if _, ok := env[f]; !ok {
						t.Errorf("%s frame %v is missing %q — the payload was dropped on the wire", tt.eventType, env, f)
					}
				}
			}
			if seen == 0 {
				t.Fatalf("%s contains no %s frame; re-capture it", tt.fixture, tt.eventType)
			}
		})
	}
}

// TestWireFixtureLevelsAreUsable pins what a client may assume about the level
// it now receives: a finite number in [0, 1] that actually moves.
func TestWireFixtureLevelsAreUsable(t *testing.T) {
	var levels []float64
	for _, env := range wireFixture(t, "voice_turn.jsonl") {
		if env["type"] != "audio_level" {
			continue
		}
		lvl, ok := env["level"].(float64)
		if !ok {
			t.Fatalf("level = %v (%T), want a JSON number", env["level"], env["level"])
		}
		if lvl < 0 || lvl > 1 || math.IsNaN(lvl) {
			t.Errorf("level = %v, want a finite value in [0,1]", lvl)
		}
		if env["source"] != "input" {
			t.Errorf("source = %v, want \"input\" for a mic capture", env["source"])
		}
		levels = append(levels, lvl)
	}
	if len(levels) < 2 {
		t.Fatalf("only %d audio_level frames captured; a meter needs a series", len(levels))
	}
	if levels[0] == levels[len(levels)-1] {
		t.Errorf("levels never changed (%v); the capture carries no signal", levels)
	}
}

// TestWireFixtureDeltasReconstructTheReply proves the deltas are the reply and
// not a truncated echo of it: concatenating them must yield response_ready.
func TestWireFixtureDeltasReconstructTheReply(t *testing.T) {
	var deltas strings.Builder
	final := ""
	for _, env := range wireFixture(t, "text_turn.jsonl") {
		switch env["type"] {
		case "response_delta":
			text, ok := env["text"].(string)
			if !ok {
				t.Fatalf("response_delta text = %v (%T), want a string", env["text"], env["text"])
			}
			deltas.WriteString(text)
		case "response_ready":
			final, _ = env["response"].(string)
		}
	}
	if final == "" {
		t.Fatal("fixture has no response_ready to compare against")
	}
	if got := strings.TrimSpace(deltas.String()); got != final {
		t.Errorf("deltas joined = %q, want %q", got, final)
	}
}
