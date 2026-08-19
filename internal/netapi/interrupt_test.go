package netapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// frameReader drains one stream socket in the background. A test cannot read
// with a per-call deadline: a timed-out read tears the socket down, so the
// next control write on the same connection fails.
type frameReader struct {
	types chan string
}

func readFrames(t *testing.T, ctx context.Context, ws *websocket.Conn) *frameReader {
	t.Helper()
	r := &frameReader{types: make(chan string, 256)}
	go func() {
		defer close(r.types)
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var env map[string]any
			if err := json.Unmarshal(data, &env); err != nil {
				return
			}
			typ, _ := env["type"].(string)
			r.types <- typ
		}
	}()
	return r
}

// await collects frame types until want arrives or d elapses, returning
// everything seen so it can be asserted against.
func (r *frameReader) await(want string, d time.Duration) []string {
	deadline := time.After(d)
	var got []string
	for {
		select {
		case typ, ok := <-r.types:
			if !ok {
				return got
			}
			got = append(got, typ)
			if typ == want {
				return got
			}
		case <-deadline:
			return got
		}
	}
}

func dialStream(t *testing.T, ctx context.Context, addr string, creds *Credentials) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+creds.Token)
	ws, _, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream", &websocket.DialOptions{
		HTTPClient: insecureClient(),
		HTTPHeader: header,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.Close(websocket.StatusNormalClosure, "") })
	return ws
}

func writeControl(t *testing.T, ctx context.Context, ws *websocket.Conn, msg controlMessage) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write %s: %v", msg.Type, err)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestRemoteInterruptEmitsTurnEvents is OV0005 E2. The idle case comes first:
// telling a client a turn was interrupted when none was running is the error
// that would put its state machine in a state the server is not in.
func TestRemoteInterruptEmitsTurnEvents(t *testing.T) {
	tests := []struct {
		name      string
		startTurn bool
		speaking  bool
		want      []string
		notWant   []string
	}{
		{
			name:    "idle interrupt resets audio only",
			want:    []string{"audio_reset"},
			notWant: []string{"turn_interrupted", "speaking_interrupted"},
		},
		{
			name:      "interrupt while thinking reports the turn, not speech",
			startTurn: true,
			want:      []string{"audio_reset", "turn_interrupted"},
			notWant:   []string{"speaking_interrupted"},
		},
		{
			name:      "interrupt while speaking reports both",
			startTurn: true,
			speaking:  true,
			want:      []string{"audio_reset", "speaking_interrupted", "turn_interrupted"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := events.NewBus()
			runner := &scriptedRunner{block: true, runs: make(chan struct{}, 1)}
			_, addr, creds := startTestServer(t, runner, bus)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			ws := dialStream(t, ctx, addr, creds)
			frames := readFrames(t, ctx, ws)
			// Audio frames only reach clients that opted in, and audio_reset
			// rides the same queue.
			writeControl(t, ctx, ws, controlMessage{Type: "audio_output", Mode: "stream"})
			if got := frames.await("audio_output_ack", 2*time.Second); !contains(got, "audio_output_ack") {
				t.Fatalf("no audio_output_ack, got %v", got)
			}

			if tt.startTurn {
				writeControl(t, ctx, ws, controlMessage{Type: "text_input", Text: "hello"})
				select {
				case <-runner.runs:
				case <-ctx.Done():
					t.Fatal("turn never started")
				}
			}
			if tt.speaking {
				bus.Emit(events.SpeakingStarted{Text: "a sentence"})
				if got := frames.await("speaking_started", 2*time.Second); !contains(got, "speaking_started") {
					t.Fatalf("speaking_started never arrived, got %v", got)
				}
			}

			writeControl(t, ctx, ws, controlMessage{Type: "interrupt"})
			got := frames.await("turn_interrupted", 2*time.Second)

			for _, want := range tt.want {
				if !contains(got, want) {
					t.Errorf("missing %s; frames = %v", want, got)
				}
			}
			for _, never := range tt.notWant {
				if contains(got, never) {
					t.Errorf("unexpected %s; frames = %v", never, got)
				}
			}
		})
	}
}

// TestRemoteInterruptReachesEveryClient is the D3 contract the app depends on:
// a barge-in on one device must return the others to armed, so the events are
// broadcast rather than answered only to the sender.
func TestRemoteInterruptReachesEveryClient(t *testing.T) {
	bus := events.NewBus()
	runner := &scriptedRunner{block: true, runs: make(chan struct{}, 1)}
	_, addr, creds := startTestServer(t, runner, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	talker := dialStream(t, ctx, addr, creds)
	listener := readFrames(t, ctx, dialStream(t, ctx, addr, creds))

	writeControl(t, ctx, talker, controlMessage{Type: "text_input", Text: "hello"})
	select {
	case <-runner.runs:
	case <-ctx.Done():
		t.Fatal("turn never started")
	}

	writeControl(t, ctx, talker, controlMessage{Type: "interrupt"})
	if got := listener.await("turn_interrupted", 3*time.Second); !contains(got, "turn_interrupted") {
		t.Fatalf("second client never saw turn_interrupted; frames = %v", got)
	}
}

// TestHubSpeakingTracksTurnBoundaries pins the flag the interrupt path reads.
// The failure that matters is the false positive: reporting speech after the
// turn ended would make every idle interrupt claim it cut off the assistant.
func TestHubSpeakingTracksTurnBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		sequence []events.Event
		want     bool
	}{
		{"nothing emitted", nil, false},
		{"thinking only", []events.Event{events.ThinkingStarted{}}, false},
		{"turn ended after speech, nobody streaming", []events.Event{events.SpeakingStarted{}, events.TurnMetrics{Outcome: "completed"}}, false},
		{"turn failed before metrics", []events.Event{events.SpeakingStarted{}, events.Error{Stage: "turn", Message: "boom"}}, false},
		{"interrupted speech", []events.Event{events.SpeakingStarted{}, events.SpeakingInterrupted{Reason: "x"}}, false},
		{"next turn started", []events.Event{events.SpeakingStarted{}, events.ThinkingStarted{}}, false},
		{"speaking", []events.Event{events.ThinkingStarted{}, events.SpeakingStarted{}}, true},
		{"between segments of one reply", []events.Event{events.SpeakingStarted{}, events.SpeakingComplete{}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := events.NewBus()
			h := newHub()
			detach := h.attachBus(bus)
			defer detach()
			for _, e := range tt.sequence {
				bus.Emit(e)
			}
			if got := h.speaking.Load(); got != tt.want {
				t.Errorf("speaking = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestInterruptFixturesMatchTheContract reads the two live interrupt captures
// back. A unit test can be satisfied by a mock; these frames left a real
// socket, so they catch an event that never reached the wire at all.
func TestInterruptFixturesMatchTheContract(t *testing.T) {
	tests := []struct {
		fixture string
		want    []string
		notWant []string
	}{
		{
			fixture: "interrupt_idle.jsonl",
			want:    []string{"audio_reset"},
			notWant: []string{"turn_interrupted", "speaking_interrupted"},
		},
		{
			fixture: "interrupt_speaking.jsonl",
			want:    []string{"audio_reset", "speaking_interrupted", "turn_interrupted"},
		},
		{
			// The host called this turn complete before the interrupt landed;
			// the client was still playing it. Captured live.
			fixture: "interrupt_after_delivery.jsonl",
			want:    []string{"turn_metrics", "audio_reset", "speaking_interrupted", "turn_interrupted"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			var types []string
			reasons := map[string]string{}
			for _, env := range wireFixture(t, tt.fixture) {
				typ, _ := env["type"].(string)
				types = append(types, typ)
				if reason, ok := env["reason"].(string); ok && reason == remoteInterruptReason {
					reasons[typ] = reason
				}
			}
			for _, want := range tt.want {
				if !contains(types, want) {
					t.Errorf("%s missing %s; frames = %v", tt.fixture, want, types)
				}
			}
			for _, never := range tt.notWant {
				if contains(types, never) {
					t.Errorf("%s has an unexpected %s; frames = %v", tt.fixture, never, types)
				}
			}
			if len(tt.notWant) == 0 {
				for _, typ := range []string{"speaking_interrupted", "turn_interrupted"} {
					if reasons[typ] != remoteInterruptReason {
						t.Errorf("%s reason = %q, want %q so a client can tell a remote barge-in from the host mic", typ, reasons[typ], remoteInterruptReason)
					}
				}
			}
		})
	}
}

// TestHubSpeakingSurvivesHostPlaybackForStreamClients is the regression for
// the case that made E2 useless where it matters. With a muted host and a
// stream client, the fanout finishes as soon as the chunks are queued, so
// speaking_complete and turn_metrics land about a millisecond after
// speaking_started while the client still has ~20 s of audio to play. The
// server must not conclude from that that nothing is being spoken.
func TestHubSpeakingSurvivesHostPlaybackForStreamClients(t *testing.T) {
	tests := []struct {
		name     string
		streamer bool
		want     bool
	}{
		{"host player only — turn_metrics really is the end", false, false},
		{"stream client still playing — turn_metrics is not", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := events.NewBus()
			h := newHub()
			detach := h.attachBus(bus)
			defer detach()

			conn := newTestConn(4, 4)
			h.add(conn)
			h.setConnAudio(conn, tt.streamer)

			bus.Emit(events.ThinkingStarted{})
			bus.Emit(events.SpeakingStarted{Text: "a long reply"})
			bus.Emit(events.SpeakingComplete{Elapsed: time.Millisecond})
			bus.Emit(events.TurnMetrics{Outcome: "completed"})

			if got := h.speaking.Load(); got != tt.want {
				t.Errorf("speaking = %v, want %v", got, tt.want)
			}
			// Either way, the reset boundary ends it.
			h.resetAudio()
			if h.speaking.Load() {
				t.Error("speaking survived an audio reset")
			}
		})
	}
}

// TestInterruptAfterWireDeliveryReportsTheTurn drives the whole path: the turn
// is over as far as the host is concerned, the client is still hearing it, and
// the barge-in must still be announced. A repeated interrupt must not be.
func TestInterruptAfterWireDeliveryReportsTheTurn(t *testing.T) {
	bus := events.NewBus()
	runner := &scriptedRunner{runs: make(chan struct{}, 1)}
	_, addr, creds := startTestServer(t, runner, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := dialStream(t, ctx, addr, creds)
	frames := readFrames(t, ctx, ws)
	writeControl(t, ctx, ws, controlMessage{Type: "audio_output", Mode: "stream"})
	frames.await("audio_output_ack", 2*time.Second)

	// A turn that completes on the host while the client still has audio.
	writeControl(t, ctx, ws, controlMessage{Type: "text_input", Text: "say something long"})
	<-runner.runs
	bus.Emit(events.ThinkingStarted{})
	bus.Emit(events.SpeakingStarted{Text: "a long reply"})
	bus.Emit(events.SpeakingComplete{Elapsed: time.Millisecond})
	bus.Emit(events.TurnMetrics{Outcome: "completed"})
	frames.await("turn_metrics", 2*time.Second)

	writeControl(t, ctx, ws, controlMessage{Type: "interrupt"})
	got := frames.await("turn_interrupted", 2*time.Second)
	for _, want := range []string{"audio_reset", "speaking_interrupted", "turn_interrupted"} {
		if !contains(got, want) {
			t.Errorf("missing %s after a barge-in over streamed audio; frames = %v", want, got)
		}
	}

	// The second interrupt has nothing left to report.
	writeControl(t, ctx, ws, controlMessage{Type: "interrupt"})
	again := frames.await("turn_interrupted", 700*time.Millisecond)
	if contains(again, "turn_interrupted") || contains(again, "speaking_interrupted") {
		t.Errorf("a repeated interrupt announced a second cancellation; frames = %v", again)
	}
	if !contains(again, "audio_reset") {
		t.Errorf("a repeated interrupt must still reset audio; frames = %v", again)
	}
}
