package stt

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

func TestCompletionPlaybackRetentionRestartsElapsedStartTimeout(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, nearMiss := range []bool{false, true} {
			name := map[bool]string{false: "offline/", true: "streaming/"}[streaming] + map[bool]string{false: "confirm", true: "near_miss"}[nearMiss]
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					policy := endpoint.FromConfig(&config.Config{}, false)
					if policy.StartTimeout != 10*time.Second {
						t.Fatalf("production start timeout=%s, want 10s", policy.StartTimeout)
					}
					// The source remains live during the pause. An explicit final
					// frame ends the retained fixture after sufficient speech.
					policy.FinalizeOnEOF = true
					h := newPlaybackLoopHarness(t, streaming, policy)
					at := time.Now()
					h.attempt.Render.Observe(at)
					parked := completionTestFrame(0, at.Add(time.Second), 0)
					h.frames.push(parked)
					h.waitRead(t)
					h.commandBarrier(t)
					synctest.Wait()

					// Virtual time passes while the actual loop is parked. The
					// playback interval must not exhaust the resumed listen budget.
					time.Sleep(policy.StartTimeout + time.Second)
					select {
					case <-h.done:
						t.Fatal("STT ended while completion capture was parked")
					default:
					}
					want := slices.Clone(parked.Samples)
					for i := range 3 {
						frame := completionTestFrame(uint64((i+1)*1600), at.Add(time.Duration(1100+i*100)*time.Millisecond), .1)
						want = append(want, frame.Samples...)
						h.frames.push(frame)
					}
					h.frames.push(audio.Frame{Final: true})
					h.commandBarrier(t)
					select {
					case <-h.frames.reads:
						t.Fatal("parked loop consumed the unread qualifying speech")
					default:
					}

					var err error
					if nearMiss {
						err = h.session.FinishCompletionPlayback(h.ctx, h.token, h.attempt.ID, true)
					} else {
						err = h.session.ConfirmCompletionPlayback(h.ctx, h.token, h.attempt.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					got := h.waitDone(t)
					final, ok := terminal(got).(FinalTranscript)
					if !ok || final.Text != "retained" {
						t.Fatalf("resumed silence timed out before unread speech: terminal=%#v", terminal(got))
					}
					terminals := 0
					for _, event := range got {
						switch event.(type) {
						case FinalTranscript, Timeout, Failure:
							terminals++
						}
					}
					if terminals != 1 {
						t.Fatalf("terminal deliveries=%d, want one final transcript", terminals)
					}
					actual := h.decoded
					if streaming {
						actual = h.rec.pcm
					}
					if !slices.Equal(actual, want) {
						t.Fatalf("retained silence/speech changed: got%d samples, want%d", len(actual), len(want))
					}
				})
			})
		}
	}
}
