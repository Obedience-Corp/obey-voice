package playback

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

type fillerIntegrationSTT struct{}
type fillerIntegrationSession struct{ events chan stt.Event }

func (s *fillerIntegrationSession) Events() <-chan stt.Event { return s.events }
func (*fillerIntegrationSession) Close() error               { return nil }
func (*fillerIntegrationSTT) Available() bool                { return true }
func (*fillerIntegrationSTT) Start(context.Context) (stt.Session, error) {
	events := make(chan stt.Event, 1)
	events <- stt.FinalTranscript{Text: "Please answer."}
	close(events)
	return &fillerIntegrationSession{events}, nil
}

type fillerIntegrationBrain struct{ calls int }

func (b *fillerIntegrationBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	b.calls++
	if b.calls == 1 { // Prime the public rolling first-audio latency policy.
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	chunks := make(chan string, 1)
	chunks <- "The complete real answer is ready."
	close(chunks)
	done := make(chan brain.StreamResult, 1)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}
func (*fillerIntegrationBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (*fillerIntegrationBrain) ClearHistory()            {}
func (*fillerIntegrationBrain) History() []brain.Turn    { return nil }
func (*fillerIntegrationBrain) LoadHistory([]brain.Turn) {}

type fillerIntegrationTTS struct {
	mu      sync.Mutex
	answers int
	blocked chan struct{}
	release chan struct{}
}

func (*fillerIntegrationTTS) Available() bool                       { return true }
func (*fillerIntegrationTTS) ListVoices(string, string) []tts.Voice { return nil }
func (s *fillerIntegrationTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	sample := float32(.2) // cached filler
	if text == "The complete real answer is ready." {
		sample = .6
		s.mu.Lock()
		s.answers++
		answer := s.answers
		s.mu.Unlock()
		if answer == 2 {
			close(s.blocked)
			<-s.release // Intentionally ignores cancellation, modeling late synthesis.
		}
	}
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24000); err != nil {
		return nil, err
	}
	go func() {
		defer stream.Close()
		_ = stream.Write([]float32{sample, sample, sample, sample, sample, sample, sample, sample})
	}()
	return stream, nil
}

func fillerWait(t *testing.T, what string, ready func() bool) {
	t.Helper()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-tick.C:
		case <-timeout.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
func fillerQueued(p *Player) *playbackSegment {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != nil {
		return p.current
	}
	if len(p.queue) > 0 {
		return p.queue[0]
	}
	return nil
}
func fillerRunTurn(p *pipeline.Pipeline, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { _, err := p.RunTurn(ctx); done <- err }()
	return done
}
func fillerFinishTurn(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("voice turn did not finish")
	}
}

// This crosses the real Pipeline -> Player boundary: a player double that
// ignores playback-generation contexts cannot catch foreground invalidation.
func TestBackchannelPipelinePreservesForegroundGeneration(t *testing.T) {
	for _, state := range []string{"active", "completed", "reset_during_answer"} {
		t.Run(state, func(t *testing.T) {
			player := NewPlayer()
			defer player.Close()
			player.testOpen = func(rate int) (nativeOutput, int, int, error) { return &fakeOutput{}, rate, 2, nil }
			speech := &fillerIntegrationTTS{blocked: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			releaseAnswer := func() { releaseOnce.Do(func() { close(speech.release) }) }
			t.Cleanup(releaseAnswer)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			pipe := &pipeline.Pipeline{STT: &fillerIntegrationSTT{}, Brain: &fillerIntegrationBrain{}, TTS: speech, Player: player}
			pipe.EnableBackchannel(ctx)
			prime := fillerRunTurn(pipe, ctx)
			fillerWait(t, "priming answer", func() bool { return fillerQueued(player) != nil })
			player.onData(make([]byte, 8*2*2), nil, 8)
			fillerFinishTurn(t, prime)

			turn := fillerRunTurn(pipe, ctx)
			select {
			case <-speech.blocked:
			case <-ctx.Done():
				t.Fatal("answer synthesis never entered")
			}
			fillerWait(t, "filler admission", func() bool { return fillerQueued(player) != nil })
			filler := fillerQueued(player)
			player.mu.Lock()
			epoch := player.epoch
			player.mu.Unlock()
			frames := uint32(1)
			if state == "completed" {
				frames = 8
			}
			player.onData(make([]byte, int(frames)*2*2), nil, frames)
			if state == "completed" {
				select {
				case <-filler.finishedCh:
				case <-ctx.Done():
					t.Fatal("filler did not finish")
				}
			}
			if state == "reset_during_answer" {
				if err := pipe.ResetAudioOutput(ctx); err != nil {
					t.Fatal(err)
				}
			}
			releaseAnswer()
			if state == "reset_during_answer" {
				fillerFinishTurn(t, turn)
				if segment := fillerQueued(player); segment != nil {
					t.Fatal("pre-reset answer was admitted")
				}
				out := make([]byte, 8*2*2)
				player.onData(out, nil, 8)
				for _, b := range out {
					if b != 0 {
						t.Fatal("stale answer rendered after reset")
					}
				}
				return
			}
			// Cancellation of an active filler must terminate that one segment. A
			// naturally completed filler must not invalidate the next admission either.
			select {
			case <-filler.finishedCh:
			case <-ctx.Done():
				t.Fatal("filler cancellation did not finish")
			}
			fillerWait(t, "foreground answer admission", func() bool {
				player.mu.Lock()
				defer player.mu.Unlock()
				for _, segment := range player.queue {
					if segment != filler {
						return true
					}
				}
				return player.current != nil && player.current != filler
			})
			player.mu.Lock()
			newEpoch := player.epoch
			player.mu.Unlock()
			if newEpoch != epoch {
				t.Fatalf("filler cancellation changed foreground epoch: %d -> %d", epoch, newEpoch)
			}
			out := make([]byte, 8*2*2)
			player.onData(out, nil, 8)
			for offset := 0; offset < len(out); offset += 2 {
				// Answer PCM (.6) is distinct from filler (.2), and must occupy the
				// entire next buffer without waiting for the filler's remaining duration.
				if sample := int16(binary.LittleEndian.Uint16(out[offset:])); sample < 19000 {
					t.Fatalf("answer replaced/delayed by filler or silence: %d", sample)
				}
			}
			fillerFinishTurn(t, turn)
		})
	}
}
