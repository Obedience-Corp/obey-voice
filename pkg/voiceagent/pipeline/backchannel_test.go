package pipeline

import (
	"context"
	"math/rand"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// Error cases first: every state in which a filler must NOT play. A filler on a
// fast turn is the failure mode that makes this feel scripted, and it is worse
// than the silence it was meant to cover.

func TestShouldNotPlay(t *testing.T) {
	clip := backchannelClip{phrase: "Mm-hm.", samples: []float32{0, 0}, rate: 24000}

	tests := []struct {
		name string
		b    *backchannel
	}{
		{
			// The first turn of a session has no history. Guessing on no
			// evidence is exactly what the heuristic exists to avoid, and a
			// wrong guess on turn one is the worst possible first impression.
			name: "no history yet",
			b:    &backchannel{clips: []backchannelClip{clip}},
		},
		{
			name: "turns are fast",
			b: &backchannel{clips: []backchannelClip{clip}, recent: []time.Duration{
				200 * time.Millisecond, 300 * time.Millisecond, 250 * time.Millisecond,
			}},
		},
		{
			// Exactly at the threshold is not above it.
			name: "exactly at the threshold",
			b: &backchannel{clips: []backchannelClip{clip}, recent: []time.Duration{
				backchannelThreshold, backchannelThreshold,
			}},
		},
		{
			// Every phrase failed to synthesize. Nothing to play, and the gate
			// must not pretend otherwise.
			name: "empty phrase pool",
			b:    &backchannel{recent: []time.Duration{5 * time.Second, 5 * time.Second}},
		},
		{
			// One slow outlier among fast turns must not start the agent
			// filling every turn — that is why the gate uses a median.
			name: "single slow outlier among fast turns",
			b: &backchannel{clips: []backchannelClip{clip}, recent: []time.Duration{
				200 * time.Millisecond, 10 * time.Second, 250 * time.Millisecond,
				300 * time.Millisecond, 220 * time.Millisecond,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.b.shouldPlay() {
				t.Error("shouldPlay() = true; a filler here is worse than silence")
			}
		})
	}
}

func TestShouldPlayWhenTurnsAreConsistentlySlow(t *testing.T) {
	b := &backchannel{
		clips: []backchannelClip{{phrase: "Mm-hm.", rate: 24000}},
		recent: []time.Duration{
			2 * time.Second, 2400 * time.Millisecond, 1800 * time.Millisecond,
		},
	}
	if !b.shouldPlay() {
		t.Error("shouldPlay() = false with a 2s median; this is the case the feature exists for")
	}
}

// The rolling window must actually roll, or the gate answers with startup
// conditions forever.
func TestObserveKeepsOnlyTheRecentWindow(t *testing.T) {
	b := &backchannel{clips: []backchannelClip{{phrase: "Mm-hm."}}}

	// Fill the window with slow turns, then push it out with fast ones.
	for range backchannelWindow {
		b.observe(3 * time.Second)
	}
	if !b.shouldPlay() {
		t.Fatal("expected the gate to open after a run of slow turns")
	}
	for range backchannelWindow {
		b.observe(100 * time.Millisecond)
	}
	if b.shouldPlay() {
		t.Error("the gate stayed open after turns got fast; the window is not rolling")
	}
	if len(b.recent) != backchannelWindow {
		t.Errorf("recent holds %d entries, want %d", len(b.recent), backchannelWindow)
	}
}

// A zero or negative duration is a metric that was never stamped. Recording it
// would drag the median down and silently disable the feature.
func TestObserveIgnoresUnstampedMetrics(t *testing.T) {
	b := &backchannel{clips: []backchannelClip{{phrase: "Mm-hm."}}}
	b.observe(0)
	b.observe(-1 * time.Second)
	if len(b.recent) != 0 {
		t.Errorf("recent = %v, want empty — an unstamped metric is not a fast turn", b.recent)
	}
}

// Repetition is what turns a humanising touch into an obvious loop.
func TestPickNeverRepeatsConsecutively(t *testing.T) {
	b := &backchannel{lastIdx: -1, rnd: deterministicRand()}
	for _, p := range []string{"a", "b", "c", "d"} {
		b.clips = append(b.clips, backchannelClip{phrase: p})
	}

	prev := ""
	for i := range 200 {
		clip, ok := b.pick()
		if !ok {
			t.Fatal("pick() returned no clip from a non-empty pool")
		}
		if clip.phrase == prev {
			t.Fatalf("iteration %d: %q played twice in a row", i, clip.phrase)
		}
		prev = clip.phrase
	}
}

// With one phrase there is no alternative, so the no-repeat rule cannot be
// honoured. It must still return the clip rather than deadlock looking for a
// different one.
func TestPickWithASinglePhrase(t *testing.T) {
	b := &backchannel{lastIdx: -1, rnd: deterministicRand()}
	b.clips = []backchannelClip{{phrase: "only"}}
	for range 3 {
		clip, ok := b.pick()
		if !ok || clip.phrase != "only" {
			t.Fatalf("pick() = %q, %v; want the single clip", clip.phrase, ok)
		}
	}
}

func TestPickFromEmptyPool(t *testing.T) {
	b := &backchannel{lastIdx: -1, rnd: deterministicRand()}
	if _, ok := b.pick(); ok {
		t.Error("pick() reported a clip from an empty pool")
	}
}

func TestMedianDuration(t *testing.T) {
	tests := []struct {
		name string
		in   []time.Duration
		want time.Duration
	}{
		{"odd count", []time.Duration{3, 1, 2}, 2},
		{"even count averages the middle pair", []time.Duration{4, 1, 3, 2}, 2},
		{"single", []time.Duration{7}, 7},
		{"already sorted", []time.Duration{1, 2, 3, 4, 5}, 3},
		{"reverse sorted", []time.Duration{5, 4, 3, 2, 1}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := medianDuration(tt.in); got != tt.want {
				t.Errorf("medianDuration(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// A nil backchannel is the default state — the feature is off unless configured.
// Every entry point must tolerate it.
func TestPlayBackchannelIsANoOpWhenDisabled(t *testing.T) {
	p := &Pipeline{}
	p.playBackchannel(t.Context()) // must not panic
}

func deterministicRand() *rand.Rand { return rand.New(rand.NewSource(1)) }

type backchannelVoice struct {
	fakeTTS
	sample      float32
	unavailable bool
	calls       int
	holdAt      int
	hold        chan struct{}
}

func (v *backchannelVoice) Available() bool { return !v.unavailable }

func (v *backchannelVoice) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	v.calls++
	if v.calls == v.holdAt && v.hold != nil {
		<-v.hold
	}
	stream := audio.NewPCMStream(ctx)
	defer stream.Close()
	if err := stream.SetSampleRate(24000); err != nil {
		return nil, err
	}
	return stream, stream.Write([]float32{v.sample})
}

func recordSlowBackchannelTurn(p *Pipeline, final time.Time) {
	m := newTurnMetrics()
	m.sttFinal = final
	event := playbackEvent{kind: playbackStarted, playStarted: final.Add(2 * time.Second)}
	p.applyPlaybackEvent(event, m, &atomic.Int64{})
}

func expectBackchannel(t *testing.T, p *Pipeline, sink *recordingSink, want ...float32) {
	t.Helper()
	started := 0
	remove := events.SubscribeDetachable(p.Events, func(events.BackchannelStarted) { started++ })
	defer remove()
	sink.samples = nil
	p.playBackchannel(t.Context())
	synctest.Wait()
	if started != len(want) || !slices.Equal(sink.samples, want) {
		t.Fatalf("backchannel requests = %d, PCM = %v, want %d and %v", started, sink.samples, len(want), want)
	}
}

func TestBackchannelVoiceReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &recordingSink{}
		p := &Pipeline{TTS: &backchannelVoice{sample: .1}, Player: sink, Events: events.NewBus()}
		p.EnableBackchannel(t.Context())
		oldFinal := time.Now()
		recordSlowBackchannelTurn(p, oldFinal)
		expectBackchannel(t, p, sink, .1)

		time.Sleep(time.Millisecond)
		next := &backchannelVoice{sample: .2, holdAt: 2, hold: make(chan struct{})}
		p.ReplaceTTS(next, nil)
		synctest.Wait()
		recordSlowBackchannelTurn(p, time.Now())
		expectBackchannel(t, p, sink)
		close(next.hold)
		synctest.Wait()
		expectBackchannel(t, p, sink, .2)

		p.ReplaceTTS(&backchannelVoice{sample: .3}, nil)
		synctest.Wait()
		recordSlowBackchannelTurn(p, oldFinal)
		expectBackchannel(t, p, sink)
		recordSlowBackchannelTurn(p, time.Now())
		expectBackchannel(t, p, sink, .3)
	})
}

func TestBackchannelIgnoresSupersededBuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &recordingSink{}
		p := &Pipeline{Player: sink, Events: events.NewBus()}
		p.EnableBackchannel(t.Context())
		slow := &backchannelVoice{sample: .1, holdAt: len(backchannelPhrases), hold: make(chan struct{})}
		p.ReplaceTTS(slow, nil)
		synctest.Wait()
		p.ReplaceTTS(&backchannelVoice{sample: .2}, nil)
		synctest.Wait()
		recordSlowBackchannelTurn(p, time.Now())
		if len(p.TTSRetentions(slow, nil)) == 0 {
			t.Fatal("retired provider can close while synthesis is still running")
		}
		close(slow.hold)
		synctest.Wait()
		expectBackchannel(t, p, sink, .2)
		if len(p.TTSRetentions(slow, nil)) != 0 {
			t.Fatal("retired provider still held after synthesis returned")
		}
	})
}

func TestBackchannelBrainReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &recordingSink{}
		voice := &backchannelVoice{sample: .1}
		p := &Pipeline{TTS: voice, Player: sink, Events: events.NewBus()}
		p.EnableBackchannel(t.Context())
		oldFinal := time.Now()
		recordSlowBackchannelTurn(p, oldFinal)
		next, err := p.PrepareConversationBrain(&fakeBrain{}, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		expectBackchannel(t, p, sink, .1)
		calls := voice.calls
		time.Sleep(time.Millisecond)
		if err := next.Commit(nil); err != nil {
			t.Fatal(err)
		}
		recordSlowBackchannelTurn(p, oldFinal)
		expectBackchannel(t, p, sink)
		recordSlowBackchannelTurn(p, time.Now())
		expectBackchannel(t, p, sink, .1)
		if voice.calls != calls {
			t.Fatal("brain replacement synthesized new voice clips")
		}
	})
}

func TestBackchannelMutedOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &recordingSink{}
		p := &Pipeline{TTS: &backchannelVoice{sample: .1}, Player: sink, Events: events.NewBus()}
		p.EnableBackchannel(t.Context())
		recordSlowBackchannelTurn(p, time.Now())
		p.SetOutputMuted(true)
		expectBackchannel(t, p, sink)
		p.SetOutputMuted(false)
		expectBackchannel(t, p, sink, .1)
	})
}

func TestBackchannelReplacementWhileDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		voice := &backchannelVoice{sample: .1}
		sink := &recordingSink{}
		p := &Pipeline{Player: sink, Events: events.NewBus()}
		p.ReplaceTTS(voice, nil)
		synctest.Wait()
		recordSlowBackchannelTurn(p, time.Now())
		expectBackchannel(t, p, sink)
		if voice.calls != 0 {
			t.Fatal("disabled backchannel requested synthesis")
		}
	})
}

func TestBackchannelRequiresAvailablePrimary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		voice := &backchannelVoice{unavailable: true}
		fallback := &backchannelVoice{sample: .2}
		sink := &recordingSink{}
		p := &Pipeline{TTS: &backchannelVoice{sample: .1}, Player: sink, Events: events.NewBus()}
		p.EnableBackchannel(t.Context())
		p.ReplaceTTS(nil, fallback)
		synctest.Wait()
		recordSlowBackchannelTurn(p, time.Now())
		expectBackchannel(t, p, sink)
		p.ReplaceTTS(voice, fallback)
		synctest.Wait()
		recordSlowBackchannelTurn(p, time.Now())
		expectBackchannel(t, p, sink)
		if voice.calls != 0 || fallback.calls != 0 {
			t.Fatalf("synthesis requests: unavailable primary %d, fallback %d", voice.calls, fallback.calls)
		}
	})
}

type pendingClipVoice struct {
	fakeTTS
	ctx    context.Context
	stream *audio.PCMStream
}

func (v *pendingClipVoice) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	v.ctx = ctx
	v.stream = audio.NewPCMStream(ctx)
	return v.stream, nil
}

func TestBackchannelBuildLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lifetime, cancel := context.WithCancel(t.Context())
		defer cancel()
		sink := &recordingSink{}
		p := &Pipeline{Player: sink, Events: events.NewBus()}
		p.EnableBackchannel(lifetime)
		voice := &pendingClipVoice{}
		p.ReplaceTTS(voice, nil)
		synctest.Wait()
		cancel()
		synctest.Wait()
		if voice.ctx == nil || voice.ctx.Err() != context.Canceled {
			t.Fatal("session cancellation did not reach pending clip")
		}

		drained := make(chan struct{})
		go func() {
			p.DrainBackchannel(voice)
			close(drained)
		}()
		synctest.Wait()
		select {
		case <-drained:
			t.Fatal("provider released before its PCM producer finished")
		default:
		}
		voice.stream.CloseWithError(voice.ctx.Err())
		<-drained
		recordSlowBackchannelTurn(p, time.Now())
		expectBackchannel(t, p, sink)
	})
}

type queuedBackchannelSink struct {
	sinkEngine
	admitted context.Context
	started  chan struct{}
}

func (s *queuedBackchannelSink) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	stream.Drain(ctx)
	s.admitted = ctx
	return audio.NewPlayback(s.started, nil), nil
}

func TestBackchannelReplacementCancelsSelectedClip(t *testing.T) {
	for _, action := range []string{"replace", "mute"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := &queuedBackchannelSink{}
				p := &Pipeline{TTS: &backchannelVoice{sample: .1}, Player: sink, Events: events.NewBus()}
				p.EnableBackchannel(t.Context())
				recordSlowBackchannelTurn(p, time.Now())
				started := 0
				events.Subscribe(p.Events, func(events.BackchannelStarted) { started++ })
				play := p.prepareBackchannelPlayback(t.Context())
				if play == nil {
					t.Fatal("expected a clip to be selected")
				}
				if action == "replace" {
					p.ReplaceTTS(&backchannelVoice{sample: .2}, nil)
				} else {
					p.SetOutputMuted(true)
				}
				go play()
				synctest.Wait()
				if started != 0 || sink.admitted != nil {
					t.Fatalf("canceled clip reached player %v, starts %d", sink.admitted != nil, started)
				}
			})
		})
	}
}

func TestBackchannelMuteDuringPlayback(t *testing.T) {
	for _, state := range []string{"queued", "playing"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := &queuedBackchannelSink{started: make(chan struct{})}
				p := &Pipeline{TTS: &backchannelVoice{sample: .1}, Player: sink, Events: events.NewBus()}
				p.EnableBackchannel(t.Context())
				recordSlowBackchannelTurn(p, time.Now())
				starts := 0
				events.Subscribe(p.Events, func(events.BackchannelStarted) { starts++ })
				p.playBackchannel(t.Context())
				synctest.Wait()
				if sink.admitted == nil || starts != 0 {
					t.Fatalf("queued clip admitted %v, starts %d", sink.admitted != nil, starts)
				}
				want := 0
				if state == "playing" {
					close(sink.started)
					synctest.Wait()
					want = 1
				}
				p.SetOutputMuted(true)
				synctest.Wait()
				if sink.admitted.Err() != context.Canceled || starts != want {
					t.Fatalf("playback context error %v, starts %d, want canceled and %d", sink.admitted.Err(), starts, want)
				}
			})
		})
	}
}

type serialBackchannelVoice struct {
	backchannelVoice
	slot      chan struct{}
	release   chan struct{}
	requested chan string
}

func (v *serialBackchannelVoice) Synthesize(ctx context.Context, phrase string) (*audio.PCMStream, error) {
	v.requested <- phrase
	v.slot <- struct{}{}
	defer func() { <-v.slot }()
	if phrase == backchannelPhrases[0] {
		<-v.release
	}
	return v.backchannelVoice.Synthesize(ctx, phrase)
}

type backchannelSTT struct {
	fakeSTT
	listening chan chan stt.Event
}

func (s *backchannelSTT) Start(context.Context) (stt.Session, error) {
	input := make(chan stt.Event, 1)
	s.listening <- input
	return &fakeSTTSession{events: input}, nil
}

func TestBackchannelBuildDuringBackToBackVoiceTurns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const answer = "This is the spoken answer."
		voice := &serialBackchannelVoice{
			slot: make(chan struct{}, 1), release: make(chan struct{}),
			requested: make(chan string, len(backchannelPhrases)+2),
		}
		mic := &backchannelSTT{listening: make(chan chan stt.Event, 1)}
		player := newFakePlayer(0)
		defer player.Close()
		finishPlayback := player.HoldPlayback()
		p := &Pipeline{STT: mic, Brain: &fakeBrain{chunks: []string{answer}}, Player: player, Events: events.NewBus()}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		p.EnableBackchannel(ctx)
		thinking, proceed := make(chan struct{}, 1), make(chan struct{})
		events.Subscribe(p.Events, func(events.ThinkingStarted) {
			thinking <- struct{}{}
			select {
			case <-proceed:
			case <-ctx.Done():
			}
		})
		starts := 0
		events.Subscribe(p.Events, func(events.BackchannelStarted) { starts++ })
		done := make(chan error, 1)
		go func() {
			for range 2 {
				if _, err := p.RunTurn(ctx); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()

		first := <-mic.listening
		p.ReplaceTTS(voice, nil)
		synctest.Wait()
		if len(voice.requested) != 1 || <-voice.requested != backchannelPhrases[0] {
			t.Fatal("open capture prevented synthesis with the new voice")
		}
		first <- stt.FinalTranscript{Text: "first question"}
		<-thinking
		close(voice.release)
		synctest.Wait()
		if len(voice.requested) != 0 {
			t.Fatal("build requested another phrase after the final transcript")
		}
		proceed <- struct{}{}
		<-player.StartedSignal()
		synctest.Wait()
		if len(voice.requested) != 1 || <-voice.requested != answer {
			t.Fatal("build competed with the playing reply")
		}

		finishPlayback()
		second := <-mic.listening
		synctest.Wait()
		if len(voice.requested) != len(backchannelPhrases)-1 {
			t.Fatalf("second capture is still open, got %d remaining phrase requests", len(voice.requested))
		}
		recordSlowBackchannelTurn(p, time.Now())
		second <- stt.FinalTranscript{Text: "second question"}
		<-thinking
		synctest.Wait()
		if starts != 1 {
			t.Fatalf("second turn played %d thinking sounds, want one from the rebuilt pool", starts)
		}
		proceed <- struct{}{}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestBackchannelBuildDuringTranscription(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mic := &backchannelSTT{listening: make(chan chan stt.Event, 1)}
		p := &Pipeline{STT: mic, Player: &sinkEngine{}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		p.EnableBackchannel(ctx)
		done := make(chan error, 1)
		go func() { _, err := p.RunTranscription(ctx); done <- err }()
		input := <-mic.listening
		voice := &backchannelVoice{}
		p.ReplaceTTS(voice, nil)
		synctest.Wait()
		if voice.calls != len(backchannelPhrases) {
			t.Fatalf("open transcription allowed %d phrase requests, want %d", voice.calls, len(backchannelPhrases))
		}
		input <- stt.FinalTranscript{Text: "transcribed input"}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestBackchannelWaitingBuildCancelsDuringTypedSpeech(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		player := newFakePlayer(0)
		defer player.Close()
		finishPlayback := player.HoldPlayback()
		p := &Pipeline{Brain: &fakeBrain{chunks: []string{"This is the answer."}}, Player: player, TTS: &backchannelVoice{}}
		lifetime, cancel := context.WithCancel(t.Context())
		defer cancel()
		p.EnableBackchannel(lifetime)
		done := make(chan error, 1)
		go func() { done <- p.RunTurnTextMode(t.Context(), "question") }()
		<-player.StartedSignal()
		voice := &backchannelVoice{}
		p.ReplaceTTS(voice, nil)
		synctest.Wait()
		cancel()
		synctest.Wait()
		if voice.calls != 0 || len(p.TTSRetentions(voice, nil)) != 0 {
			t.Fatal("canceled speech wait synthesized or retained a provider")
		}
		finishPlayback()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestBackchannelBuildYieldsToNarration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		player := newFakePlayer(0)
		defer player.Close()
		finishFirst := player.HoldPlayback()
		p := &Pipeline{Player: player, TTS: &fakeTTS{}}
		p.EnableBackchannel(t.Context())
		n := &Narrator{pipeline: p, active: true, resumeCh: make(chan struct{}, 1), units: testNarrateUnits("First span.", "Second span.")}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go n.playLoop(ctx, cancel)
		<-player.StartedSignal()
		next := &fakeTTS{}
		p.ReplaceTTS(next, nil)
		synctest.Wait()
		if got := next.Texts(); len(got) != 0 {
			t.Fatalf("build ran during narration playback: %q", got)
		}
		finishSecond := player.HoldPlayback()
		finishFirst()
		<-player.StartedSignal()
		synctest.Wait()
		if got := next.Texts(); !slices.Equal(got, []string{"Second span."}) {
			t.Fatalf("expected only the second narration span, got %q", got)
		}
		n.handlePause()
		synctest.Wait()
		if got := next.Texts(); !slices.Equal(got, append([]string{"Second span."}, backchannelPhrases...)) {
			t.Fatalf("paused narration did not yield to the build: %q", got)
		}
		finishSecond()
	})
}

func TestBackchannelConcurrentReplacement(t *testing.T) {
	voice := &fakeTTS{}
	p := &Pipeline{TTS: voice, Player: &sinkEngine{}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p.EnableBackchannel(ctx)
	recordSlowBackchannelTurn(p, time.Now())
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, action := range []func(int){
		func(int) { p.ReplaceTTS(voice, nil) },
		func(int) { p.playBackchannel(ctx) },
		func(i int) { p.SetOutputMuted(i%2 == 0) },
		func(int) { recordSlowBackchannelTurn(p, time.Now()) },
	} {
		workers.Go(func() {
			<-start
			for i := range 128 {
				action(i)
			}
		})
	}
	close(start)
	workers.Wait()
	cancel()
	p.DrainBackchannel(voice)
}
