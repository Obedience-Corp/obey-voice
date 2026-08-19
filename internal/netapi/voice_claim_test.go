package netapi

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// voiceRunner is a TurnRunner that can also run voice turns, so a test can
// drive the remote-mic path without STT.
type voiceRunner struct {
	mu       sync.Mutex
	texts    []string
	voices   int
	consumed int // samples read off the ingress by voice turns
	finals   int // Final frames seen

	block      bool // park every turn until ctx cancellation
	drain      bool // read the ingress like a real STT loop would
	ingress    *audio.Ingress
	textStart  chan struct{}
	voiceStart chan struct{}
	voiceDone  chan struct{}
}

func (r *voiceRunner) RunTurnTextMode(ctx context.Context, input string) error {
	r.mu.Lock()
	r.texts = append(r.texts, input)
	r.mu.Unlock()
	if r.textStart != nil {
		r.textStart <- struct{}{}
	}
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (r *voiceRunner) RunTurn(ctx context.Context) (string, error) {
	r.mu.Lock()
	r.voices++
	r.mu.Unlock()
	if r.voiceStart != nil {
		r.voiceStart <- struct{}{}
	}
	if r.drain {
		err := r.drainIngress(ctx)
		if r.voiceDone != nil {
			r.voiceDone <- struct{}{}
		}
		return "", err
	}
	if r.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "", nil
}

// drainIngress reads until the utterance is finalized, the way the real STT
// loop consumes remote PCM.
func (r *voiceRunner) drainIngress(ctx context.Context) error {
	for {
		frame, err := r.ingress.ReadFrame(ctx)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.consumed += len(frame.Samples)
		if frame.Final {
			r.finals++
		}
		r.mu.Unlock()
		if frame.Final {
			return nil
		}
	}
}

func (r *voiceRunner) samplesConsumed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.consumed
}

func (r *voiceRunner) voiceTurns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.voices
}

// startVoiceServer is startTestServer with a remote-mic ingress attached, and
// it hands back the server so a test can inspect the claim the hub holds.
func startVoiceServer(t *testing.T, runner *voiceRunner) (*Server, *Dispatcher, string, *Credentials) {
	t.Helper()
	return startVoiceServerWith(t, runner, nil)
}

// startVoiceServerWith lets a test tune Options before the server is built.
// The turn deadlines above all: no test should wait out ten real seconds to
// watch a silent client's claim come back.
func startVoiceServerWith(t *testing.T, runner *voiceRunner, tune func(*Options)) (*Server, *Dispatcher, string, *Credentials) {
	t.Helper()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	bus := events.NewBus()
	d := NewDispatcher(runner, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	ingress := audio.NewIngress()
	t.Cleanup(func() { ingress.Close() })
	runner.ingress = ingress

	opts := Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  d,
		Ingress:     ingress,
		Providers:   Providers{Brain: "claude", TTS: "kokoro"},
	}
	if tune != nil {
		tune(&opts)
	}
	s := New(opts)
	go func() {
		if err := s.ListenAndServe(ctx); err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	}()

	deadline := time.After(2 * time.Second)
	for s.Addr() == nil {
		select {
		case <-deadline:
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
	return s, d, s.Addr().String(), creds
}

func micClaimed(h *hub) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.micClaimant != nil
}

// eventually polls cond because the control reader runs on the server's own
// goroutine: a write returning does not mean the server has acted on it.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// consistently checks cond stays true for a short window — used for "no claim
// appeared" assertions, which a single poll cannot prove.
func consistently(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s stopped holding", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestQueuedVoiceStartTakesNoClaim is OV0005 E3. The lock-out is the failure
// that matters: a voice_start queued behind someone else's turn used to claim
// the mic on arrival, so a false trigger on one device silenced every other
// device until it disconnected.
func TestQueuedVoiceStartTakesNoClaim(t *testing.T) {
	runner := &voiceRunner{block: true, textStart: make(chan struct{}, 1)}
	s, _, addr, creds := startVoiceServer(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := dialStream(t, ctx, addr, creds)
	aFrames := readFrames(t, ctx, a)
	b := dialStream(t, ctx, addr, creds)
	bFrames := readFrames(t, ctx, b)

	// A holds the pipeline with a turn that never finishes on its own.
	writeControl(t, ctx, a, controlMessage{Type: "text_input", Text: "hold the line"})
	select {
	case <-runner.textStart:
	case <-ctx.Done():
		t.Fatal("turn never started")
	}

	// B fires a voice_start behind it.
	writeControl(t, ctx, b, controlMessage{Type: "voice_start"})
	consistently(t, "the mic unclaimed while B's turn is only queued", func() bool {
		return !micClaimed(s.hub)
	})

	// A's own next voice_start must not be refused because of B.
	writeControl(t, ctx, a, controlMessage{Type: "voice_start"})
	if got := aFrames.await("error", 500*time.Millisecond); contains(got, "error") {
		t.Errorf("A's voice_start was refused while B was merely queued; frames = %v", got)
	}
	if got := bFrames.await("error", 200*time.Millisecond); contains(got, "error") {
		t.Errorf("B's queued voice_start reported an error; frames = %v", got)
	}
}

// TestQueuedVoiceTurnClaimsOnDequeue is the other half: deferring the claim
// must not lose the turn. When the queue reaches it, it takes the mic and runs.
func TestQueuedVoiceTurnClaimsOnDequeue(t *testing.T) {
	runner := &voiceRunner{
		block:      true,
		textStart:  make(chan struct{}, 1),
		voiceStart: make(chan struct{}, 1),
	}
	s, _, addr, creds := startVoiceServer(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := dialStream(t, ctx, addr, creds)
	b := dialStream(t, ctx, addr, creds)

	writeControl(t, ctx, a, controlMessage{Type: "text_input", Text: "hold the line"})
	<-runner.textStart

	writeControl(t, ctx, b, controlMessage{Type: "voice_start"})
	consistently(t, "the mic unclaimed while queued", func() bool { return !micClaimed(s.hub) })

	// Free the pipeline; B's turn is next in line.
	writeControl(t, ctx, a, controlMessage{Type: "interrupt"})
	select {
	case <-runner.voiceStart:
	case <-ctx.Done():
		t.Fatal("queued voice turn never ran")
	}
	eventually(t, "the dequeued turn to hold the mic", func() bool { return micClaimed(s.hub) })
}

// TestQueuedVoiceStartReleasesOnDisconnect covers the leak the review gate
// looks for: a client that vanishes while its turn is queued must not have the
// mic handed to it when the queue reaches it.
func TestQueuedVoiceStartReleasesOnDisconnect(t *testing.T) {
	runner := &voiceRunner{block: true, textStart: make(chan struct{}, 1)}
	s, _, addr, creds := startVoiceServer(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := dialStream(t, ctx, addr, creds)
	b := dialStream(t, ctx, addr, creds)

	writeControl(t, ctx, a, controlMessage{Type: "text_input", Text: "hold the line"})
	<-runner.textStart

	writeControl(t, ctx, b, controlMessage{Type: "voice_start"})
	consistently(t, "the mic unclaimed while queued", func() bool { return !micClaimed(s.hub) })

	b.Close(websocket.StatusNormalClosure, "gone")
	writeControl(t, ctx, a, controlMessage{Type: "interrupt"})

	consistently(t, "the mic unclaimed after the queued client left", func() bool {
		return !micClaimed(s.hub)
	})
	if runner.voiceTurns() != 0 {
		t.Errorf("voice turns run = %d, want 0 — a turn ran for a client that had gone", runner.voiceTurns())
	}
}

// TestQueuedVoiceTurnKeepsItsAudio is the backward-compatibility guarantee a
// deferred claim would otherwise break. A client streams the moment it has
// sent voice_start — the bundled web client does exactly this — so the frames
// that arrive while the turn waits must reach the turn, not the floor.
func TestQueuedVoiceTurnKeepsItsAudio(t *testing.T) {
	runner := &voiceRunner{
		block:      true,
		drain:      true,
		textStart:  make(chan struct{}, 1),
		voiceStart: make(chan struct{}, 1),
		voiceDone:  make(chan struct{}, 1),
	}
	s, _, addr, creds := startVoiceServer(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := dialStream(t, ctx, addr, creds)
	b := dialStream(t, ctx, addr, creds)
	bFrames := readFrames(t, ctx, b)

	writeControl(t, ctx, a, controlMessage{Type: "text_input", Text: "hold the line"})
	<-runner.textStart

	// B talks straight through the queue: voice_start, PCM, voice_end.
	writeControl(t, ctx, b, controlMessage{Type: "voice_start"})
	consistently(t, "the mic unclaimed while queued", func() bool { return !micClaimed(s.hub) })
	const frames, samplesPerFrame = 4, 1600
	for i := 0; i < frames; i++ {
		writeControl(t, ctx, b, controlMessage{
			Type:       "audio_input",
			Data:       pcmFrame(samplesPerFrame),
			SampleRate: 16000,
		})
	}
	writeControl(t, ctx, b, controlMessage{Type: "voice_end"})
	if got := bFrames.await("error", 700*time.Millisecond); contains(got, "error") {
		t.Fatalf("streaming into a queued turn was refused; frames = %v", got)
	}

	// Free the pipeline; B's turn runs with everything it said.
	writeControl(t, ctx, a, controlMessage{Type: "interrupt"})
	select {
	case <-runner.voiceDone:
	case <-ctx.Done():
		t.Fatal("queued voice turn never finalized")
	}
	if got, want := runner.samplesConsumed(), frames*samplesPerFrame; got != want {
		t.Errorf("samples reaching the turn = %d, want %d", got, want)
	}
	eventually(t, "the mic released after the staged utterance", func() bool { return !micClaimed(s.hub) })
}

// TestVoiceStartQueueFullLeavesNoClaim: ErrBusy must not leave a claim behind.
func TestVoiceStartQueueFullLeavesNoClaim(t *testing.T) {
	runner := &voiceRunner{block: true, textStart: make(chan struct{}, 1)}
	s, d, addr, creds := startVoiceServer(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn := dialStream(t, ctx, addr, creds)
	frames := readFrames(t, ctx, conn)

	writeControl(t, ctx, conn, controlMessage{Type: "text_input", Text: "hold the line"})
	<-runner.textStart
	for i := 0; i < dispatchQueueDepth; i++ {
		if err := d.SubmitText("filler"); err != nil {
			t.Fatalf("filling the queue: %v", err)
		}
	}

	writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
	if got := frames.await("error", 2*time.Second); !contains(got, "error") {
		t.Fatalf("a full queue accepted voice_start; frames = %v", got)
	}
	consistently(t, "the mic unclaimed after a busy refusal", func() bool { return !micClaimed(s.hub) })
}

// TestIdleAndBargeInVoiceStartClaimImmediately pins the paths that must keep
// claiming on arrival: plain push-to-talk, and the interrupt → voice_start
// barge-in. A client streams PCM as soon as it has sent voice_start, so a
// deferred claim there would drop the head of the utterance.
func TestIdleAndBargeInVoiceStartClaimImmediately(t *testing.T) {
	tests := []struct {
		name        string
		startTurn   bool
		thenInterru bool
	}{
		{name: "idle serve"},
		{name: "after interrupting an in-flight turn", startTurn: true, thenInterru: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &voiceRunner{block: true, textStart: make(chan struct{}, 1), voiceStart: make(chan struct{}, 1)}
			s, _, addr, creds := startVoiceServer(t, runner)

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			conn := dialStream(t, ctx, addr, creds)
			frames := readFrames(t, ctx, conn)

			if tt.startTurn {
				writeControl(t, ctx, conn, controlMessage{Type: "text_input", Text: "hold the line"})
				<-runner.textStart
			}
			if tt.thenInterru {
				writeControl(t, ctx, conn, controlMessage{Type: "interrupt"})
				frames.await("turn_interrupted", 2*time.Second)
			}

			writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
			eventually(t, "an immediate mic claim", func() bool { return micClaimed(s.hub) })
			if got := frames.await("error", 300*time.Millisecond); contains(got, "error") {
				t.Errorf("voice_start was refused; frames = %v", got)
			}
		})
	}
}

// TestClaimMicRefusesClosedConnection is the guard under claim-on-dequeue:
// even if a queued turn slipped through, the hub will not hand the mic to a
// connection it has already removed.
func TestClaimMicRefusesClosedConnection(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(h *hub, c *streamConn)
		wantErr string
	}{
		{
			name:    "remote mic disabled",
			setup:   func(h *hub, c *streamConn) { h.add(c) },
			wantErr: "remote mic is not enabled on this serve instance",
		},
		{
			name:    "connection never added",
			setup:   func(h *hub, c *streamConn) { h.setIngress(audio.NewIngress()) },
			wantErr: "connection is closed",
		},
		{
			name: "connection removed",
			setup: func(h *hub, c *streamConn) {
				h.setIngress(audio.NewIngress())
				h.add(c)
				h.remove(c)
			},
			wantErr: "connection is closed",
		},
		{
			name: "another client holds it",
			setup: func(h *hub, c *streamConn) {
				h.setIngress(audio.NewIngress())
				other := newTestConn(1, 1)
				h.add(other)
				h.add(c)
				if err := h.claimMic(other); err != nil {
					panic(err)
				}
			},
			wantErr: "microphone claimed by another client",
		},
		{
			name: "live connection claims",
			setup: func(h *hub, c *streamConn) {
				h.setIngress(audio.NewIngress())
				h.add(c)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHub()
			c := newTestConn(1, 1)
			tt.setup(h, c)

			err := h.claimMic(c)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("claimMic = %v, want success", err)
			case tt.wantErr == "":
				if !h.isClaimant(c) {
					t.Error("claimMic succeeded but the hub does not report the claimant")
				}
			case err == nil:
				t.Fatalf("claimMic succeeded, want %q", tt.wantErr)
			case err.Error() != tt.wantErr:
				t.Fatalf("claimMic = %q, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestBusyRefusalKeepsALiveClaim: a second voice_start that bounces off a full
// queue must not release the claim the connection is already speaking into.
// Releasing it would also finalize the utterance mid-sentence.
func TestBusyRefusalKeepsALiveClaim(t *testing.T) {
	runner := &voiceRunner{block: true, voiceStart: make(chan struct{}, 1)}
	s, d, addr, creds := startVoiceServer(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn := dialStream(t, ctx, addr, creds)
	frames := readFrames(t, ctx, conn)

	writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
	select {
	case <-runner.voiceStart:
	case <-ctx.Done():
		t.Fatal("voice turn never started")
	}
	eventually(t, "the live claim", func() bool { return micClaimed(s.hub) })

	for i := 0; i < dispatchQueueDepth; i++ {
		if err := d.SubmitText("filler"); err != nil {
			t.Fatalf("filling the queue: %v", err)
		}
	}
	writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
	if got := frames.await("error", 2*time.Second); !contains(got, "error") {
		t.Fatalf("a full queue accepted the second voice_start; frames = %v", got)
	}
	consistently(t, "the live claim after a busy refusal", func() bool { return micClaimed(s.hub) })
}

// TestVoiceEndDropsTheConnectionsOwnQueuedTurn: a connection that fires a
// second voice_start while it is still the claimant would otherwise leave a
// queued turn that claims the mic for an utterance nobody is speaking, parking
// the pipeline until that client disconnects.
func TestVoiceEndDropsTheConnectionsOwnQueuedTurn(t *testing.T) {
	runner := &voiceRunner{block: true, voiceStart: make(chan struct{}, 1)}
	s, _, addr, creds := startVoiceServer(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn := dialStream(t, ctx, addr, creds)

	writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
	<-runner.voiceStart
	eventually(t, "the live claim", func() bool { return micClaimed(s.hub) })

	// A second voice_start behind its own live turn: queued, claims nothing.
	writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
	writeControl(t, ctx, conn, controlMessage{Type: "voice_end"})
	eventually(t, "the claim to be released by voice_end", func() bool { return !micClaimed(s.hub) })

	writeControl(t, ctx, conn, controlMessage{Type: "interrupt"})
	consistently(t, "the mic unclaimed after the queue drained", func() bool { return !micClaimed(s.hub) })
	if got := runner.voiceTurns(); got != 1 {
		t.Errorf("voice turns run = %d, want 1 — the abandoned second turn ran", got)
	}
}

// pcmFrame builds one base64 frame of silence-shaped PCM: content does not
// matter here, only that the samples survive the queue.
func pcmFrame(n int) string {
	raw := make([]byte, n*2)
	for i := range raw {
		raw[i] = byte(i % 251)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// TestDisconnectOrderingLeavesNoClaim pins the invariant that makes the
// disconnect path safe now that a claim can be granted from the dispatch
// goroutine: removing the connection first means a later claim is refused, and
// releasing after means a claim granted just before the removal is still
// cleared. Either interleaving must end with the mic free.
func TestDisconnectOrderingLeavesNoClaim(t *testing.T) {
	tests := []struct {
		name  string
		claim func(h *hub, c *streamConn)
	}{
		{
			name: "claim granted just before the connection was removed",
			claim: func(h *hub, c *streamConn) {
				if err := h.claimMic(c); err != nil {
					t.Fatalf("claimMic: %v", err)
				}
			},
		},
		{
			name:  "claim never granted",
			claim: func(h *hub, c *streamConn) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHub()
			h.setIngress(audio.NewIngress())
			c := newTestConn(1, 1)
			h.add(c)
			tt.claim(h, c)

			// The order handleStream's defer uses.
			h.remove(c)
			h.releaseMic(c)

			if micClaimed(h) {
				t.Error("a claim survived the disconnect")
			}
			if err := h.claimMic(c); err == nil {
				t.Error("a removed connection was granted the mic")
			}
		})
	}
}
