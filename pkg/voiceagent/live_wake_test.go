package voiceagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/kws"
)

// nameSample marks a chunk in which fakeListener hears the name.
const nameSample = float32(0.5)

// fakeListener hears "Juno" in every chunk that starts with nameSample.
type fakeListener struct {
	mu     sync.Mutex
	chunks int
	resets int
	closed bool
	err    error
}

func (l *fakeListener) Write(samples []float32) ([]kws.Hit, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.chunks++
	if l.err != nil {
		return nil, l.err
	}
	if len(samples) > 0 && samples[0] == nameSample {
		return []kws.Hit{{Name: "Juno", By: time.Second}}, nil
	}
	return nil, nil
}

func (l *fakeListener) Reset() { l.mu.Lock(); l.resets++; l.mu.Unlock() }
func (l *fakeListener) Close() { l.mu.Lock(); l.closed = true; l.mu.Unlock() }
func (l *fakeListener) state() (chunks, resets int, closed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.chunks, l.resets, l.closed
}

// micCapture is an injected microphone a test speaks into.
type micCapture struct {
	mu   sync.Mutex
	subs map[int]chan []float32
	next int
}

func (c *micCapture) Subscribe(buffer int) (int, <-chan []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subs == nil {
		c.subs = map[int]chan []float32{}
	}
	c.next++
	ch := make(chan []float32, buffer)
	c.subs[c.next] = ch
	return c.next, ch
}

func (c *micCapture) Unsubscribe(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.subs[id]; ok {
		delete(c.subs, id)
		close(ch)
	}
}

func (c *micCapture) Reset() {}

func (c *micCapture) say(samples ...float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.subs {
		ch <- samples
	}
}

func (c *micCapture) listeners() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs)
}

// liveOptions is a host with its own microphone and recogniser and no
// speaker, asking for the live wake word.
func liveOptions(mic *micCapture, listener *fakeListener) Options {
	opts := Options{
		Config: &config.Config{AgentName: "Juno", NameSummonEnabled: true, AcousticNameEnabled: true},
		Events: events.NewBus(), Brain: &stubBrain{}, Silent: true, Capture: mic, STT: &stubSTT{},
		NameSummonLive: true,
	}
	opts.nameListener = func(names []string) (nameListener, error) {
		if len(names) != 1 || names[0] != "juno" {
			return nil, errors.New("listener was not given the gate's names")
		}
		return listener, nil
	}
	return opts
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// The default: the name heard live is reported at once, and changes nothing
// about which utterances are answered.
func TestLiveNameIsACueAndNotAnAnswer(t *testing.T) {
	mic, listener := &micCapture{}, &fakeListener{}
	heard, answering := &scriptedSTT{}, &countingBrain{}
	opts := liveOptions(mic, listener)
	opts.STT, opts.Brain = heard, answering
	var mu sync.Mutex
	var cues []events.NameHeard
	events.Subscribe(opts.Events, func(e events.NameHeard) { mu.Lock(); cues = append(cues, e); mu.Unlock() })
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(cues) }

	agent, cleanup, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mic.say(0, 0)
	waitFor(t, "the listener to take quiet audio", func() bool { chunks, _, _ := listener.state(); return chunks == 1 })
	if count() != 0 {
		t.Fatal("a cue was given for audio without the name")
	}
	mic.say(nameSample, 0)
	waitFor(t, "the cue", func() bool { return count() == 1 })
	mu.Lock()
	cue := cues[0]
	mu.Unlock()
	if cue.Name != "Juno" || time.Since(cue.At) > time.Minute {
		t.Fatalf("cue = %+v", cue)
	}

	// "tell me about Juno" fires the listener and must still be ignored.
	heard.next = "tell me about Juno"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := agent.RunTurn(ctx); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if got := answering.count(); got != 0 {
		t.Fatalf("a live hit alone let an utterance through (%d brain calls)", got)
	}

	cleanup()
	if _, _, closed := listener.state(); !closed || mic.listeners() != 0 {
		t.Fatalf("cleanup left the listener open (closed=%v) or subscribed (%d)", closed, mic.listeners())
	}
}

// With a wake window the name opens the gate for what is said next.
func TestLiveNameOpensTheGateForAWakeWindow(t *testing.T) {
	mic, listener := &micCapture{}, &fakeListener{}
	heard, answering := &scriptedSTT{}, &countingBrain{}
	opts := liveOptions(mic, listener)
	opts.STT, opts.Brain = heard, answering
	opts.NameSummonWake = time.Minute
	agent, cleanup, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()
	say := func(text string) {
		t.Helper()
		heard.next = text
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := agent.RunTurn(ctx); err != nil {
			t.Fatalf("RunTurn(%q): %v", text, err)
		}
	}

	say(unaddressed)
	if got := answering.count(); got != 0 {
		t.Fatalf("the gate was open before the wake word (%d brain calls)", got)
	}
	mic.say(nameSample)
	waitFor(t, "the wake", agent.NameSummon().Awake)
	say(unaddressed)
	if got := answering.count(); got != 1 {
		t.Fatalf("a request after the wake word made %d brain calls, want 1", got)
	}
}

func TestNewRejectsALiveWakeThatCannotRun(t *testing.T) {
	change := func(edit func(*Options)) Options {
		opts := liveOptions(&micCapture{}, &fakeListener{})
		edit(&opts)
		return opts
	}
	for why, tc := range map[string]struct {
		opts Options
		want string
	}{
		"no acoustic_name_enabled": {change(func(o *Options) { o.Config.AcousticNameEnabled = false }), "NameSummonLive"},
		"no name summon":           {change(func(o *Options) { o.Config.NameSummonEnabled = false }), "NameSummonLive"},
		"host decides addressing":  {change(func(o *Options) { o.ExternalAddressing = true }), "NameSummonLive"},
		"no microphone":            {change(func(o *Options) { o.TextOnly = true }), "NameSummonLive"},
		"wake without live":        {change(func(o *Options) { o.NameSummonLive, o.NameSummonWake = false, time.Second }), "NameSummonWake"},
		"negative wake":            {change(func(o *Options) { o.NameSummonWake = -time.Second }), "NameSummonWake"},
		"model missing": {change(func(o *Options) {
			o.nameListener = func([]string) (nameListener, error) { return nil, errors.New("no keyword model") }
		}), "no keyword model"},
	} {
		if _, _, err := New(context.Background(), tc.opts); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: New = %v, want an error naming %s", why, err, tc.want)
		}
	}
}

// The agent's own voice saying its name must not be reported, while it plays
// or in the moment after.
func TestLiveWakeIgnoresTheAgentsOwnVoice(t *testing.T) {
	now := time.Unix(1000, 0)
	playing := false
	var cues int
	w := &liveWake{
		listener: &fakeListener{}, logf: t.Logf,
		playing: func() bool { return playing },
		heard:   func(kws.Hit, time.Time) { cues++ },
		now:     func() time.Time { return now },
	}
	name := []float32{nameSample}

	playing = true
	w.write(name)
	playing = false
	now = now.Add(playbackEchoTail - time.Millisecond)
	w.write(name)
	if cues != 0 {
		t.Fatalf("%d cues for the name heard during or just after a reply", cues)
	}
	now = now.Add(time.Millisecond)
	w.write(name)
	if cues != 1 {
		t.Fatalf("%d cues once the reply was over, want 1", cues)
	}
}

// frameMic is a capture that reports lost audio, as audio.Capture does.
type frameMic struct {
	micCapture
	frames chan audio.Frame
}

func (c *frameMic) SubscribeFrames(int) (int, <-chan audio.Frame) { return 99, c.frames }
func (c *frameMic) Unsubscribe(id int) {
	if id == 99 {
		close(c.frames)
	}
}

func TestLiveWakeStartsAfreshAfterLostAudio(t *testing.T) {
	mic := &frameMic{frames: make(chan audio.Frame, 4)}
	listener := &fakeListener{}
	var cues int
	stop := startLiveWake(mic, &liveWake{listener: listener, logf: t.Logf, heard: func(kws.Hit, time.Time) { cues++ }})

	mic.frames <- audio.Frame{Samples: []float32{0}}
	mic.frames <- audio.Frame{Samples: []float32{nameSample}, CaptureGap: audio.CaptureGap{SubscriptionDroppedSamples: 320, SubscriptionDroppedCallbacks: 1}}
	mic.frames <- audio.Frame{Samples: []float32{0}, CaptureGap: audio.CaptureGap{DeviceDiscontinuity: true}}
	stop()
	stop()
	chunks, resets, closed := listener.state()
	if chunks != 3 || resets != 2 || !closed || cues != 1 {
		t.Fatalf("chunks=%d resets=%d closed=%v cues=%d, want 3, 2, true, 1", chunks, resets, closed, cues)
	}
	if mic.listeners() != 0 {
		t.Fatal("a capture that reports gaps was also subscribed the plain way")
	}
}

// A listener that fails keeps the agent running and says so once.
func TestLiveWakeReportsAFailingListenerOnce(t *testing.T) {
	var logs []string
	w := &liveWake{
		listener: &fakeListener{err: errors.New("native stream gone")},
		logf:     func(format string, _ ...any) { logs = append(logs, format) },
		heard:    func(kws.Hit, time.Time) { t.Fatal("a failing listener reported a name") },
		now:      time.Now,
	}
	w.write([]float32{nameSample})
	w.write([]float32{nameSample})
	if len(logs) != 1 {
		t.Fatalf("%d log lines, want 1", len(logs))
	}
}

// The same wiring with nothing faked but the microphone: New loads the keyword
// model from the models directory, and recorded speech arrives in capture-sized
// chunks. Explicit path: no downloads, devices or user configuration.
func TestLiveWakeWithTheRealModel(t *testing.T) {
	dir := os.Getenv("OBEY_KWS_MODEL_DIR")
	if dir == "" {
		t.Skip("set OBEY_KWS_MODEL_DIR to the extracted keyword model")
	}
	dir = filepath.Clean(dir)
	if filepath.Base(dir) != config.AcousticNameModelDir {
		t.Skipf("OBEY_KWS_MODEL_DIR must end in %s for New to find it", config.AcousticNameModelDir)
	}
	mic := &micCapture{}
	heard, answering := &scriptedSTT{}, &countingBrain{}
	opts := Options{
		Config: &config.Config{AgentName: "Obey", NameSummonEnabled: true, AcousticNameEnabled: true, ModelsDir: filepath.Dir(dir)},
		Events: events.NewBus(), Brain: answering, Silent: true, Capture: mic, STT: heard,
		NameSummonLive: true, NameSummonWake: time.Minute,
	}
	var mu sync.Mutex
	var cues []string
	events.Subscribe(opts.Events, func(e events.NameHeard) { mu.Lock(); cues = append(cues, e.Name); mu.Unlock() })
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(cues) }
	agent, cleanup, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	play := func(file string) {
		t.Helper()
		samples, rate, err := audio.ReadWAVFloat32(filepath.Join("../../testdata/kws", file+".wav"))
		if err != nil || rate != 16000 {
			t.Fatalf("%s: rate %d, %v", file, rate, err)
		}
		samples = append(samples, make([]float32, 2*16000)...) // the quiet after speech
		for len(samples) > 0 {
			n := min(len(samples), 320)
			mic.say(samples[:n]...)
			samples = samples[n:]
		}
	}
	run := func(text string) {
		t.Helper()
		heard.next = text
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := agent.RunTurn(ctx); err != nil {
			t.Fatalf("RunTurn(%q): %v", text, err)
		}
	}

	play("negative")
	run(unaddressed)
	if count() != 0 || answering.count() != 0 {
		t.Fatalf("speech without the name gave %d cues and %d answers", count(), answering.count())
	}
	play("british") // "Obey, can you help me?"
	waitFor(t, "the name to be heard", func() bool { return count() == 1 })
	mu.Lock()
	name := cues[0]
	mu.Unlock()
	if !slices.Contains(agent.NameSummon().Names(), name) {
		t.Fatalf("cue named %q, which is not one of %q", name, agent.NameSummon().Names())
	}
	run(unaddressed)
	if answering.count() != 1 || count() != 1 {
		t.Fatalf("after one wake word: %d brain calls and %d cues, want 1 and 1", answering.count(), count())
	}
}
