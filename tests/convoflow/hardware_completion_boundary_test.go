package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestCompletionBoundaryRejectsFalseDeliveryEvidence(t *testing.T) {
	healthy := func() completionBoundaryPhase {
		return completionBoundaryPhase{Name: "voice", Inputs: []string{"Known phrase."}, Responses: []events.ResponseReady{{Response: "Done."}}, Metrics: []events.TurnMetrics{{Outcome: "completed", AudibleSink: audio.SinkLocalSpeaker}}, SpeakingStarted: 1, SpeakingComplete: 1, NonzeroReferenceSamples: 1600}
	}
	if err := validateCompletionBoundaryPhase(healthy(), "voice", "Known phrase."); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"no_done", "tts_fallback", "stall_watchdog", "no_pcm", "wrong_input", "duplicate_input", "degraded", "public_error", "interrupted", "timed_out", "no_metrics", "fallback", "held", "muted_render", "muted_generation"} {
		t.Run(bad, func(t *testing.T) {
			p := healthy()
			kind := "voice"
			switch bad {
			case "no_done":
				p.SpeakingComplete = 0
			case "tts_fallback":
				p.Metrics[0].TTSFallbacks = 1
			case "stall_watchdog":
				p.Metrics[0].StallWatchdogFired = true
			case "no_pcm":
				p.NonzeroReferenceSamples = 0
			case "wrong_input":
				p.Inputs[0] = "Other phrase"
			case "duplicate_input":
				p.Inputs = append(p.Inputs, p.Inputs[0])
			case "degraded":
				p.Metrics[0].Degraded = true
			case "public_error":
				p.Errors = []events.Error{{Stage: "stt", Message: "failed"}}
			case "interrupted":
				p.Metrics[0].Interrupted = true
			case "timed_out":
				p.Metrics[0].Outcome = "timed_out"
			case "no_metrics":
				p.Metrics = nil
			case "fallback":
				p.Deliveries = []events.WorkDelivery{{Stage: "announcement.fallback"}}
			case "held":
				p.Deliveries = []events.WorkDelivery{{Stage: "announcement.held"}}
			case "muted_render":
				kind = "muted"
				p = completionBoundaryPhase{NonzeroReferenceSamples: 1}
			case "muted_generation":
				kind = "muted"
				p = completionBoundaryPhase{Metrics: []events.TurnMetrics{{Outcome: "completed"}}}
			}
			if validateCompletionBoundaryPhase(p, kind, "Known phrase.") == nil {
				t.Fatal("invalid delivery accepted")
			}
		})
	}
	revoked := completionBoundaryPhase{Metrics: []events.TurnMetrics{{Outcome: "interrupted"}}, Deliveries: []events.WorkDelivery{{Stage: "announcement.scheduled", AttemptID: "attempt", CaptureEpoch: 1}}}
	if err := validateCompletionBoundaryPhase(revoked, "revoked", ""); err != nil {
		t.Fatal(err)
	}
	revoked.NonzeroReferenceSamples = 1
	if validateCompletionBoundaryPhase(revoked, "revoked", "") == nil {
		t.Fatal("PCM delivered during canceled generation accepted")
	}
}

func TestCompletionBoundaryGenerationGateRequiresCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	gate := &completionGenerationGate{Provider: &replayBrain{}, entered: make(chan time.Time, 1), canceled: make(chan time.Time, 1)}
	done := make(chan error, 1)
	go func() { _, err := gate.Complete(ctx, brain.CompletionRequest{}); done <- err }()
	<-gate.entered
	select {
	case <-done:
		t.Fatal("gate released without cancellation")
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if gate.err != context.Canceled {
		t.Fatal(gate.err)
	}
}

// Hermetic lifecycle doubles below exercise the real Pipeline and Coordinator.
// Only this fake STT owner simulates speech by revoking its own capture permit;
// the physical control has no revocation call or synthetic transcript injection.
type completionBoundarySTT struct {
	mu             sync.Mutex
	current        *completionBoundarySession
	starts, closes atomic.Int32
	early          bool
}
type completionBoundarySession struct {
	owner            *completionBoundarySTT
	events           chan stt.Event
	mu               sync.Mutex
	coord            *work.Coordinator
	token            stt.PauseToken
	paused, captured bool
	text             string
	closed           atomic.Bool
}

func (s *completionBoundarySTT) Available() bool { return true }
func (s *completionBoundarySTT) Start(context.Context) (stt.Session, error) {
	session := &completionBoundarySession{owner: s, events: make(chan stt.Event, 16)}
	s.mu.Lock()
	s.current = session
	s.mu.Unlock()
	s.starts.Add(1)
	session.events <- stt.PhaseEvent{Phase: "listening"}
	if s.early {
		session.events <- stt.Timeout{}
	}
	return session, nil
}
func (s *completionBoundarySession) Events() <-chan stt.Event { return s.events }
func (s *completionBoundarySession) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		s.owner.closes.Add(1)
	}
	return nil
}
func (s *completionBoundarySession) BindCompletionCoordinator(c *work.Coordinator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coord = c
}
func (s *completionBoundarySession) HasCapturedInput() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captured
}
func (s *completionBoundarySession) PauseForCompletion(_ context.Context, scope work.WorkScope, id string) (stt.PauseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.coord == nil {
		return stt.PauseResult{Decision: stt.PauseFinalizing}, nil
	}
	permit, err := s.coord.AcquireCapturePermit(scope, id, 1)
	if err != nil {
		return stt.PauseResult{}, err
	}
	s.token = stt.PauseToken{Scope: scope, RequestID: id, Epoch: 1, Permit: permit}
	s.paused = true
	s.events <- stt.YieldedForCompletion{Scope: scope, RequestID: id, Epoch: 1, Token: s.token}
	return stt.PauseResult{Decision: stt.PauseAcknowledged, Token: s.token}, nil
}
func (s *completionBoundarySession) ResumeCapture(_ context.Context, token stt.PauseToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token != s.token {
		return errors.New("wrong retained token")
	}
	s.paused = false
	s.events <- stt.PhaseEvent{Phase: "listening"}
	if s.captured {
		s.captured = false
		s.events <- stt.FinalTranscript{Text: s.text}
	}
	return nil
}
func (s *completionBoundarySTT) speech(text string) error {
	s.mu.Lock()
	session := s.current
	s.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.paused {
		if err := session.coord.RevokeCapturePermit(session.token.Permit); err != nil {
			return err
		}
		session.captured = true
		session.text = text
	} else {
		session.events <- stt.FinalTranscript{Text: text}
	}
	return nil
}

type completionBoundaryBrain struct {
	replayBrain
	completionText string
}

func (*completionBoundaryBrain) AppendAssistantResponse(string) error { return nil }
func (b *completionBoundaryBrain) Complete(_ context.Context, r brain.CompletionRequest) (brain.CompletionEnvelope, error) {
	var e brain.CompletionEnvelope
	for _, r := range r.Results {
		if b.completionText != "" {
			r.Brief = b.completionText
		}
		e.Updates = append(e.Updates, brain.CompletionUpdate{ResultID: r.ResultID, Text: r.Brief})
	}
	return e, nil
}

type completionBoundaryTTS struct{}

func (*completionBoundaryTTS) Available() bool                       { return true }
func (*completionBoundaryTTS) ListVoices(string, string) []tts.Voice { return nil }
func (*completionBoundaryTTS) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	s := audio.NewPCMStream(ctx)
	if err := s.SetSampleRate(16000); err != nil {
		return nil, err
	}
	if err := s.Write([]float32{.1, .1, .1}); err != nil {
		return nil, err
	}
	s.Close()
	return s, nil
}

type completionBoundaryEngine struct {
	refs    atomic.Int64
	mu      sync.Mutex
	results []audio.PlaybackResult
}

func (e *completionBoundaryEngine) PlayStream(ctx context.Context, s *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		defer close(done)
		_, err := s.WaitReady(ctx)
		if err != nil {
			done <- audio.PlaybackResult{Err: err}
			return
		}
		attempt := audio.PlaybackAttemptFromContext(ctx)
		close(started)
		n := 0
		for f := range s.Frames() {
			attempt.Write(func() { n += len(f); e.refs.Add(int64(len(f))) })
		}
		// A real device starts playback before it can finish its PCM. Keep
		// those lifecycle edges distinct; an instantaneous fake makes the
		// consumer legitimately select Done before observing Started.
		time.Sleep(time.Millisecond)
		result := attempt.Result(audio.PlaybackResult{AudioSeconds: float64(n) / 16000, Err: ctx.Err()})
		e.mu.Lock()
		e.results = append(e.results, result)
		e.mu.Unlock()
		done <- result
	}()
	return audio.NewPlayback(started, done), nil
}
func (*completionBoundaryEngine) Stop()            {}
func (*completionBoundaryEngine) IsPlaying() bool  { return false }
func (*completionBoundaryEngine) Close() error     { return nil }
func (*completionBoundaryEngine) SinkKind() string { return audio.SinkLocalSpeaker }

func newCompletionBoundaryPipeline(t *testing.T) (*pipeline.Pipeline, *completionBoundarySTT, *completionBoundaryEngine) {
	t.Helper()
	p := &pipeline.Pipeline{Events: events.NewBus(), Brain: &completionBoundaryBrain{}, TTS: &completionBoundaryTTS{}}
	engine := &completionBoundaryEngine{}
	p.Player = engine
	recognizer := &completionBoundarySTT{}
	p.STT = recognizer
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	agency := pipeline.NewAgency(p, jobs, &replayBrain{}, &config.Config{}, nil)
	agency.Wire()
	if err := p.EnableConversationWork("boundary-unit", nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.CloseWork(); err != nil {
			t.Error(err)
		}
	})
	return p, recognizer, engine
}

func TestCompletionBoundaryProductionLifecycle(t *testing.T) {
	for _, mode := range []string{"acoustic_completion_mute", "acoustic_completion_speech"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, recognizer, engine := newCompletionBoundaryPipeline(t)
				dir := t.TempDir()
				start := time.Now()
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				plays := 0
				err := runCompletionBoundary(ctx, p, mode, "Known phrase.", dir, func() int64 { return int64(time.Since(start).Seconds() * 16000) }, func() int { return int(engine.refs.Load()) }, func(context.Context) (acousticReport, error) {
					plays++
					err := recognizer.speech("Known phrase.")
					time.Sleep(100 * time.Millisecond)
					a := boundaryHealthyCapture()
					return a, err
				})
				if err != nil {
					data, _ := os.ReadFile(filepath.Join(dir, "completion-boundary.json"))
					engine.mu.Lock()
					results := append([]audio.PlaybackResult(nil), engine.results...)
					engine.mu.Unlock()
					t.Fatalf("%v\nengine=%+v\n%s", err, results, data)
				}
				var report completionBoundaryReport
				data, err := os.ReadFile(filepath.Join(dir, "completion-boundary.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(data, &report); err != nil {
					t.Fatal(err)
				}
				verified := 0
				for _, phase := range report.Phases {
					if phase.WeatherFactsVerified {
						verified++
					}
				}
				if verified != 1 {
					t.Fatalf("weather public/spoken facts verified %d times, want one", verified)
				}
				wantPlays := 1
				if mode == "acoustic_completion_speech" {
					wantPlays = 2
					if !report.RetainedInput || report.GenerationEntered.IsZero() || report.GenerationCanceled.Before(report.GenerationEntered) {
						t.Fatal("missing admitted speech cancellation evidence")
					}
				}
				if plays != wantPlays || len(report.Final.Communicated) != 1 || len(report.Final.Pending) != 0 || report.Error != "" {
					t.Fatalf("plays=%d report=%+v", plays, report)
				}
				if recognizer.starts.Load() != recognizer.closes.Load() {
					t.Fatalf("capture sessions not closed starts=%d closes=%d", recognizer.starts.Load(), recognizer.closes.Load())
				}
				if report.Published.Outcome.SourceRef == "" || !strings.Contains(report.Published.Outcome.Limitations, "not a current") || report.SourceSHA256 == "" {
					t.Fatal("weather fixture provenance missing")
				}
			})
		})
	}
}

func TestCompletionBoundaryFailsStalledOrEarlyCaptureAndRetainsArtifact(t *testing.T) {
	for _, bad := range []string{"early", "stalled_reads", "canceled", "bad_fixture"} {
		t.Run(bad, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, recognizer, engine := newCompletionBoundaryPipeline(t)
				recognizer.early = bad == "early"
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				dir := t.TempDir()
				start := time.Now()
				if bad == "canceled" {
					go func() { time.Sleep(500 * time.Millisecond); cancel() }()
				}
				err := runCompletionBoundary(ctx, p, "acoustic_completion_mute", "Known phrase.", dir, func() int64 {
					if bad == "stalled_reads" {
						return 0
					}
					return int64(time.Since(start).Seconds() * 16000)
				}, func() int { return int(engine.refs.Load()) }, func(context.Context) (acousticReport, error) {
					if bad != "bad_fixture" {
						t.Error("invalid control played stimulus")
					}
					if err := recognizer.speech("Known phrase."); err != nil {
						return acousticReport{}, err
					}
					return acousticReport{}, nil
				})
				if err == nil {
					t.Fatal("invalid physical evidence passed")
				}
				data, e := os.ReadFile(filepath.Join(dir, "completion-boundary.json"))
				if e != nil {
					t.Fatal(e)
				}
				var report completionBoundaryReport
				if e = json.Unmarshal(data, &report); e != nil || report.Error == "" {
					t.Fatalf("missing failed artifact %s err=%v", data, e)
				}
				if p.OutputMuted() {
					t.Fatal("output remained muted")
				}
				if recognizer.starts.Load() != recognizer.closes.Load() {
					t.Fatal("STT owner leaked")
				}
			})
		})
	}
}

type completionBoundaryRunner struct {
	fakeRunner
	mode, wav, expected, dir string
}

func (r *completionBoundaryRunner) runAcousticCompletionBoundary(_ context.Context, mode, wav, expected, dir string) error {
	r.mode, r.wav, r.expected, r.dir = mode, wav, expected, dir
	return nil
}
func TestCompletionBoundaryScenariosAndDispatch(t *testing.T) {
	for _, op := range []string{"acoustic_completion_mute", "acoustic_completion_speech"} {
		body := "name: boundary\ninput: speech\ncapture: {microphone: true, speakers: true}\nturns:\n - id: boundary\n   op: " + op + "\n   wav: phrase.wav\n   expected_transcript: Known phrase.\n"
		scn, err := loadScenarioString("boundary", body)
		if err != nil {
			t.Fatal(err)
		}
		rec := &recorder{}
		runner := &completionBoundaryRunner{fakeRunner: fakeRunner{rec: rec, live: "route_only", liveOn: true}}
		e := &engine{scn: scn, runDir: t.TempDir(), rec: rec, runner: runner}
		if err = e.run(t.Context()); err != nil {
			t.Fatal(err)
		}
		if runner.mode != op || runner.wav != "phrase.wav" || runner.expected != "Known phrase." || runner.dir != filepath.Join(e.runDir, "acoustic", "boundary") {
			t.Fatalf("bad forwarding %+v", runner)
		}
		for _, bad := range []string{strings.Replace(body, "microphone: true", "microphone: false", 1), strings.Replace(body, "   wav: phrase.wav\n", "", 1), strings.Replace(body, "   expected_transcript: Known phrase.\n", "", 1), body + "   duration: 1s\n", body + "   user: override\n", body + "   interrupt_wav: other.wav\n", "tts: {provider: none}\n" + body, "latency_condition: baseline\n" + body} {
			if _, err := loadScenarioString("bad", bad); err == nil {
				t.Fatalf("accepted invalid boundary %s", bad)
			}
		}
	}
}

func TestCompletionBoundaryRejectsWrongOrIncompleteReceipt(t *testing.T) {
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	published := work.ResultView{Outcome: work.Outcome{Scope: scope, JobID: "job", ResultID: "result", TerminalRevision: 1}}
	valid := func() work.Snapshot {
		return work.Snapshot{Scope: scope, RetiredJobIDs: []string{"job"}, Communicated: []work.ResultView{{Outcome: published.Outcome, AnnouncementState: work.AnnouncementCommunicated, LastAttempt: &work.DeliveryEvidence{Receipt: work.DeliveryReceipt{Scope: scope, AttemptID: "attempt", CaptureEpoch: 1, Mode: "voice", Audible: true, PlaybackComplete: true, ResponseCommitted: true, Segments: 1}}}}}
	}
	if _, err := requireCompletionDelivered(valid(), published); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"wrong_result", "wrong_revision", "duplicate_result", "still_pending", "text_only", "no_commit", "no_playback", "interrupted", "zero_segments", "wrong_scope", "zero_epoch"} {
		t.Run(bad, func(t *testing.T) {
			s := valid()
			r := &s.Communicated[0]
			switch bad {
			case "wrong_result":
				r.Outcome.ResultID = "different"
			case "wrong_revision":
				r.Outcome.TerminalRevision++
			case "duplicate_result":
				s.Communicated = append(s.Communicated, *r)
			case "still_pending":
				s.Pending = []work.ResultView{published}
			case "text_only":
				r.LastAttempt.Receipt.Mode = "text"
			case "no_commit":
				r.LastAttempt.Receipt.ResponseCommitted = false
			case "no_playback":
				r.LastAttempt.Receipt.PlaybackComplete = false
			case "interrupted":
				r.LastAttempt.Receipt.Interrupted = true
			case "zero_segments":
				r.LastAttempt.Receipt.Segments = 0
			case "wrong_scope":
				r.LastAttempt.Receipt.Scope.SessionID = "different"
			case "zero_epoch":
				r.LastAttempt.Receipt.CaptureEpoch = 0
			}
			if _, err := requireCompletionDelivered(s, published); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
	pending := work.Snapshot{Scope: scope, Jobs: []work.JobView{{Key: work.JobKey{Scope: scope, JobID: "job"}}}, Pending: []work.ResultView{published}}
	pending.Pending[0].State = work.Pending
	if err := requireCompletionPending(pending, published); err != nil {
		t.Fatal(err)
	}
	pending.Pending[0].State = work.Held
	if requireCompletionPending(pending, published) == nil {
		t.Fatal("held result counted as eligible pending")
	}
}

func TestCompletionBoundaryDoesNotTreatGateDeadlineAsSpeech(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, recognizer, engine := newCompletionBoundaryPipeline(t)
		dir := t.TempDir()
		start := time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		err := runCompletionBoundary(ctx, p, "acoustic_completion_speech", "Known phrase.", dir, func() int64 { return int64(time.Since(start).Seconds() * 16000) }, func() int { return int(engine.refs.Load()) }, func(context.Context) (acousticReport, error) { return boundaryHealthyCapture(), nil })
		if err == nil {
			t.Fatal("generation deadline passed as microphone admission")
		}
		if recognizer.starts.Load() != recognizer.closes.Load() {
			t.Fatal("retained STT owner leaked on deadline")
		}
		var report completionBoundaryReport
		data, e := os.ReadFile(filepath.Join(dir, "completion-boundary.json"))
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(data, &report); e != nil {
			t.Fatal(e)
		}
		if report.RetainedInput || report.Error == "" {
			t.Fatal("invalid admission evidence")
		}
	})
}

func TestCompletionBoundaryWeatherFactsRequirePublicAndSpokenAgreement(t *testing.T) {
	valid := func() completionBoundaryPhase {
		return completionBoundaryPhase{
			Responses:      []events.ResponseReady{{Response: "The recorded Denver snapshot says clear skies and 62 degrees Fahrenheit; not a current network forecast."}},
			SpokenSegments: []string{"Historical Denver weather was sixty-two degrees Fahrenheit.", "The sky was clear."}, SpeakingStarted: 2, SpeakingComplete: 2,
		}
	}
	if err := validateCompletionWeatherSpeech(valid()); err != nil {
		t.Fatal(err)
	}
	for _, qualification := range []string{"recorded", "snapshot", "historical", "from 2024", "from twenty twenty-four"} {
		p := valid()
		p.Responses[0].Response = "Denver was clear and 62 degrees Fahrenheit, " + qualification + "."
		if err := validateCompletionWeatherSpeech(p); err != nil {
			t.Errorf("valid qualification %q: %v", qualification, err)
		}
	}
	for _, disclaimer := range []string{"not a current forecast", "not current", "not today", "not currently", "not right now", "isn't current", "isn't a current forecast"} {
		for _, channel := range []string{"public", "spoken"} {
			t.Run("disclaimer_"+disclaimer+"_"+channel, func(t *testing.T) {
				p := valid()
				text := "The recorded Denver snapshot says clear and 62 degrees Fahrenheit; this is " + disclaimer + "."
				if channel == "public" {
					p.Responses[0].Response = text
				} else {
					p.SpokenSegments = []string{text, ""}
				}
				if err := validateCompletionWeatherSpeech(p); err != nil {
					t.Fatalf("valid no-freshness disclaimer rejected: %v", err)
				}
				// Removing a valid disclaimer must not hide a separate current claim.
				if channel == "public" {
					p.Responses[0].Response += " Denver is clear today."
				} else {
					p.SpokenSegments[0] += " Denver is clear today."
				}
				if validateCompletionWeatherSpeech(p) == nil {
					t.Fatal("disclaimer concealed an explicit current claim")
				}
			})
		}
	}
	for _, bad := range []struct{ name, text string }{
		{"generic_done", "Done."},
		{"missing_city", "Recorded weather was clear and 62 degrees Fahrenheit."},
		{"wrong_city", "The historical Boston weather was clear and 62 degrees Fahrenheit."},
		{"wrong_condition", "The Denver snapshot says cloudy and 62 degrees Fahrenheit."},
		{"wrong_temperature", "The Denver snapshot says clear and 63 degrees Fahrenheit."},
		{"number_substring", "The Denver snapshot says clear and 162 degrees Fahrenheit."},
		{"no_qualification", "Denver is clear and sixty-two degrees Fahrenheit."},
		{"current_claim", "Denver is currently clear and 62 degrees Fahrenheit according to this recorded snapshot."},
		{"today_claim", "The 2024 Denver snapshot says today is clear and 62 degrees Fahrenheit."},
		{"negated_condition", "Recorded Denver weather was not clear and was 62 degrees Fahrenheit."},
		{"negated_digits", "Recorded Denver weather was clear but not 62 degrees Fahrenheit; this is not current."},
		{"negated_spoken_number", "Recorded Denver weather was clear but not sixty-two degrees Fahrenheit; this is not a current forecast."},
	} {
		for _, channel := range []string{"public", "spoken"} {
			t.Run(bad.name+"_"+channel, func(t *testing.T) {
				p := valid()
				if channel == "public" {
					p.Responses[0].Response = bad.text
				} else {
					p.SpokenSegments = []string{bad.text, ""}
				}
				if validateCompletionWeatherSpeech(p) == nil {
					t.Fatal("omitted, incorrect, or misleading weather passed")
				}
			})
		}
	}
	p := valid()
	p.SpokenSegments = nil
	if validateCompletionWeatherSpeech(p) == nil {
		t.Fatal("public response substituted for missing actual spoken text")
	}
	p = valid()
	p.SpeakingComplete--
	if validateCompletionWeatherSpeech(p) == nil {
		t.Fatal("uncompleted spoken facts accepted")
	}
}

func TestCompletionBoundaryRejectsGenericSpokenReceipt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _, engine := newCompletionBoundaryPipeline(t)
		p.Brain = &completionBoundaryBrain{completionText: "Done."}
		dir := t.TempDir()
		start := time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		err := runCompletionBoundary(ctx, p, "acoustic_completion_mute", "Known phrase.", dir, func() int64 { return int64(time.Since(start).Seconds() * 16000) }, func() int { return int(engine.refs.Load()) }, func(context.Context) (acousticReport, error) {
			t.Fatal("generic weather delivery should fail before unrelated stimulus")
			return acousticReport{}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "weather public response") {
			t.Fatalf("generic voiced response passed: %v", err)
		}
		data, e := os.ReadFile(filepath.Join(dir, "completion-boundary.json"))
		if e != nil {
			t.Fatal(e)
		}
		var report completionBoundaryReport
		if e = json.Unmarshal(data, &report); e != nil {
			t.Fatal(e)
		}
		if len(report.Final.Communicated) != 1 || report.Error == "" {
			t.Fatalf("test must reject an otherwise real scoped voice receipt: %+v", report)
		}
		for _, phase := range report.Phases {
			if phase.WeatherFactsVerified {
				t.Fatal("generic response marked weather facts verified")
			}
		}
	})
}

func TestCompletionBoundaryWeatherTemperatureUnits(t *testing.T) {
	for _, sample := range []struct {
		text string
		want bool
	}{
		{"62 degrees Fahrenheit", true},
		{"sixty-two degrees Fahrenheit", true},
		{"62 F", true},
		{"62°F", true},
		{"62F", true},
		{"62 degree F", true},
		{"62 degrees Celsius", false},
		{"62 C", false},
		{"62 kelvin", false},
		{"62 K", false},
		{"62 degrees", false},
		{"sixty-two degrees", false},
		{"62 with a Fahrenheit unit mentioned elsewhere", false},
		{"62 forecast", false},
		{"62 fahrenheitish", false},
		{"63 degrees Fahrenheit and 62 degrees Celsius", false},
		{"62 degrees Fahrenheit and 62 degrees Kelvin", false},
	} {
		for _, channel := range []string{"public", "spoken"} {
			t.Run(sample.text+"_"+channel, func(t *testing.T) {
				valid := "The recorded Denver snapshot was clear at 62 degrees Fahrenheit."
				p := completionBoundaryPhase{Responses: []events.ResponseReady{{Response: valid}}, SpokenSegments: []string{valid}, SpeakingStarted: 1, SpeakingComplete: 1}
				text := "The recorded Denver snapshot was clear at " + sample.text + "."
				if channel == "public" {
					p.Responses[0].Response = text
				} else {
					p.SpokenSegments[0] = text
				}
				err := validateCompletionWeatherSpeech(p)
				if (err == nil) != sample.want {
					t.Fatalf("unit assertion passed=%t want=%t: %v", err == nil, sample.want, err)
				}
			})
		}
	}
}
