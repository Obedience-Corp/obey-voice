package pipeline

import (
	"context"
	"math/rand"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// Everything about the backchannel lives in this file, on purpose.
//
// This is the item most likely to be removed on taste after dogfooding, and the
// plan says so up front. Keeping it in one file plus one config flag means
// abandoning it is a trivial revert rather than an archaeology exercise.
//
// What it does: when the agent is going to be slow to answer, play a short "mm-hm"
// so the user is not left listening to silence. Decision D010 authorized building
// it from measured numbers — the wait between a user finishing speaking and the
// agent starting to speak has a p50 of 2385 ms, three times the threshold at
// which this would have been cancelled as masking a wait that no longer happens.

// backchannelThreshold is the rolling p50 from final transcript to first audio
// above which a filler is worth playing.
//
// Below this the turn is fast enough that a filler would arrive on top of the
// real answer. **That is the failure mode that makes this feel scripted**, and it
// is worse than silence — so the gate is conservative and fast turns get nothing.
const backchannelThreshold = 900 * time.Millisecond

// backchannelWindow is how many recent turns the rolling p50 considers. Short
// enough to react when a model gets slower, long enough that one outlier does not
// start the agent filling every turn.
const backchannelWindow = 8

// backchannelPhrases are synthesized once per voice and held as PCM.
var backchannelPhrases = []string{
	"Mm-hm.",
	"Let me think.",
	"Okay.",
	"Right.",
	"Hmm.",
	"One sec.",
}

// backchannel holds pre-synthesized filler audio and decides when to play it.
type backchannel struct {
	cancelBuild  context.CancelFunc
	historySince time.Time

	// clips is parallel to backchannelPhrases; a phrase whose synthesis failed
	// is simply absent rather than blocking the others.
	clips []backchannelClip

	// recent is a ring of the last backchannelWindow first-audio latencies.
	recent  []time.Duration
	lastIdx int // never play the same phrase twice consecutively

	rnd *rand.Rand
}

type backchannelClip struct {
	phrase  string
	samples []float32
	rate    int
}

// A reply or sequence of narration spans holds priority across gaps between
// segments. Independent speech paths can overlap and release in either order.
type speechPriority struct {
	pipeline *Pipeline
}

func (s *speechPriority) hold(p *Pipeline) {
	if s.pipeline != nil || p.Player == nil {
		return
	}
	p.backchannelMu.Lock()
	p.prioritySpeakers++
	p.backchannelMu.Unlock()
	s.pipeline = p
}

func (s *speechPriority) release() {
	p := s.pipeline
	if p == nil {
		return
	}
	p.backchannelMu.Lock()
	p.prioritySpeakers--
	if p.prioritySpeakers == 0 {
		p.wakeBackchannelBuildLocked()
	}
	p.backchannelMu.Unlock()
	s.pipeline = nil
}

func (p *Pipeline) wakeBackchannelBuildLocked() {
	if p.speechIdle != nil {
		close(p.speechIdle)
		p.speechIdle = nil
	}
}

func (p *Pipeline) waitForSpeechGap(ctx context.Context) bool {
	for ctx.Err() == nil {
		p.backchannelMu.Lock()
		if p.prioritySpeakers == 0 || p.OutputMuted() {
			p.backchannelMu.Unlock()
			return true
		}
		if p.speechIdle == nil {
			p.speechIdle = make(chan struct{})
		}
		idle := p.speechIdle
		p.backchannelMu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return false
		}
	}
	return false
}

func (p *Pipeline) synthesizeBackchannel(ctx context.Context, synth tts.Provider) []backchannelClip {
	if ctx.Err() != nil || !synth.Available() {
		return nil
	}
	var clips []backchannelClip
	for _, phrase := range backchannelPhrases {
		if !p.waitForSpeechGap(ctx) {
			return nil
		}
		clip, err := synthesizeClip(ctx, synth, phrase)
		if err == nil {
			clips = append(clips, clip)
		}
	}
	return clips
}

func synthesizeClip(ctx context.Context, synth tts.Provider, phrase string) (backchannelClip, error) {
	stream, err := synth.Synthesize(ctx, phrase)
	if err != nil {
		return backchannelClip{}, err
	}
	// Hold the provider lease through stream closure, even if this build is
	// superseded before the provider publishes its sample rate.
	var samples []float32
	for frame := range stream.Frames() {
		samples = append(samples, frame...)
	}
	if err := stream.Err(); err != nil {
		return backchannelClip{}, err
	}
	rate, err := stream.WaitReady(ctx)
	if err != nil {
		return backchannelClip{}, err
	}
	return backchannelClip{phrase: phrase, samples: samples, rate: rate}, nil
}

// observe records a turn's first-audio latency so the rolling p50 tracks reality
// rather than a number fixed at startup.
func (b *backchannel) observe(d time.Duration) {
	if d <= 0 {
		return
	}
	b.recent = append(b.recent, d)
	if len(b.recent) > backchannelWindow {
		b.recent = b.recent[len(b.recent)-backchannelWindow:]
	}
}

// shouldPlay reports whether recent turns have been slow enough to be worth
// covering.
//
// With no history it returns false: the first turn of a session gets no filler.
// Guessing on no evidence is exactly what the heuristic exists to avoid, and a
// wrong guess on turn one is the worst possible first impression.
func (b *backchannel) shouldPlay() bool {
	if len(b.clips) == 0 || len(b.recent) == 0 {
		return false
	}
	return medianDuration(b.recent) > backchannelThreshold
}

func medianDuration(in []time.Duration) time.Duration {
	sorted := make([]time.Duration, len(in))
	copy(sorted, in)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// pick chooses a clip at random, never the same one twice in a row. Repetition
// is what turns a humanising touch into an obvious loop.
func (b *backchannel) pick() (backchannelClip, bool) {
	switch len(b.clips) {
	case 0:
		return backchannelClip{}, false
	case 1:
		b.lastIdx = 0
		return b.clips[0], true
	}
	idx := b.rnd.Intn(len(b.clips))
	if idx == b.lastIdx {
		idx = (idx + 1) % len(b.clips)
	}
	b.lastIdx = idx
	return b.clips[idx], true
}

// playBackchannel sends a filler through the normal Player path.
//
// The filler gets its own cancelable context. The first real segment cancels
// only that playback attempt before enqueueing the answer. A global Player.Stop
// would revoke the foreground answer's admission generation as well.
func (p *Pipeline) playBackchannel(ctx context.Context) {
	if play := p.prepareBackchannelPlayback(ctx); play != nil {
		go play()
	}
}

func (p *Pipeline) prepareBackchannelPlayback(ctx context.Context) func() {
	ctx = audio.BindPlaybackContext(ctx, p.Player)
	p.backchannelMu.Lock()
	defer p.backchannelMu.Unlock()
	if p.OutputMuted() || p.Player == nil || p.backchannel == nil || !p.backchannel.shouldPlay() {
		return nil
	}
	clip, ok := p.backchannel.pick()
	if !ok {
		return nil
	}

	fillerCtx, cancel := context.WithCancel(ctx)
	p.stopBackchannelLocked()
	p.backchannelCancel = cancel
	return func() {
		if fillerCtx.Err() != nil || p.OutputMuted() {
			return
		}
		stream := audio.NewPCMStream(fillerCtx)
		if err := stream.SetSampleRate(clip.rate); err != nil {
			return
		}
		err := stream.Write(clip.samples)
		stream.Close()
		if err != nil {
			return
		}
		playback, err := p.Player.PlayStream(fillerCtx, stream)
		if err != nil || playback == nil {
			return
		}
		if started, _ := playback.WaitStart(fillerCtx.Done()); started {
			p.emit(events.BackchannelStarted{Phrase: clip.phrase})
		}
	}
}

func (p *Pipeline) stopBackchannel() {
	p.backchannelMu.Lock()
	p.stopBackchannelLocked()
	p.backchannelMu.Unlock()
}

func (p *Pipeline) stopBackchannelLocked() {
	if p.backchannelCancel != nil {
		p.backchannelCancel()
		p.backchannelCancel = nil
	}
}

func (p *Pipeline) observeBackchannel(final, firstAudio time.Time) {
	p.backchannelMu.Lock()
	defer p.backchannelMu.Unlock()
	if b := p.backchannel; b != nil && !final.Before(b.historySince) {
		b.observe(firstAudio.Sub(final))
	}
}

func (p *Pipeline) resetBackchannelHistory() {
	p.backchannelMu.Lock()
	defer p.backchannelMu.Unlock()
	if b := p.backchannel; b != nil {
		b.recent = nil
		b.historySince = time.Now()
	}
}

// EnableBackchannel prepares the initial pool. ctx owns rebuilds for the session.
func (p *Pipeline) EnableBackchannel(ctx context.Context) {
	p.ttsMu.Lock()
	p.backchannelLifetime = ctx
	build := p.prepareBackchannelLocked()
	p.ttsMu.Unlock()
	if build != nil {
		build()
	}
}

// Called under ttsMu so the empty bank and its provider lease belong to the
// same voice selection. Synthesis runs after releasing the lock.
func (p *Pipeline) prepareBackchannelLocked() func() {
	if p.backchannelLifetime == nil {
		return nil
	}
	p.backchannelMu.Lock()
	defer p.backchannelMu.Unlock()
	p.stopBackchannelLocked()
	if old := p.backchannel; old != nil && old.cancelBuild != nil {
		old.cancelBuild()
	}
	now := time.Now()
	b := &backchannel{historySince: now, lastIdx: -1, rnd: rand.New(rand.NewSource(now.UnixNano()))}
	p.backchannel = b
	primary := p.TTS
	if primary == nil {
		return nil
	}

	ctx, cancel := context.WithCancel(p.backchannelLifetime)
	b.cancelBuild = cancel
	release := p.retainTTSLocked(primary, nil, cancel)
	return func() {
		defer release()
		defer cancel()
		clips := p.synthesizeBackchannel(ctx, primary)
		p.backchannelMu.Lock()
		if ctx.Err() == nil {
			b.clips = clips
		}
		p.backchannelMu.Unlock()
	}
}

// DrainBackchannel cancels and joins clip synthesis before its provider is closed.
// Shutdown can bypass worker retentions while a clip stream is still winding down.
func (p *Pipeline) DrainBackchannel(provider tts.Provider) {
	p.ttsMu.RLock()
	var pending []<-chan struct{}
	for r := range p.ttsRetentions {
		if r.cancelBuild != nil && r.primary == provider {
			r.cancelBuild()
			pending = append(pending, r.done)
		}
	}
	p.ttsMu.RUnlock()
	for _, done := range pending {
		<-done
	}
}
