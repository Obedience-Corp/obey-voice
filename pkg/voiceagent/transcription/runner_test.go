package transcription

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// fakeSource hands out scripted chunks and counts reads, so a test can watch
// the reader stop when the sink stops draining.
type fakeSource struct {
	chunks  [][]float32
	total   int64
	idx     int
	reads   atomic.Int64
	pos     atomic.Int64
	failAt  int // 1-based read that fails instead of returning a frame
	failErr error
	posBias int64 // added to Position, to fake a source that miscounts
}

func (s *fakeSource) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if err := ctx.Err(); err != nil {
		return audio.Frame{}, err
	}
	reads := s.reads.Add(1)
	if s.failAt > 0 && int(reads) == s.failAt {
		return audio.Frame{}, s.failErr
	}
	if s.idx >= len(s.chunks) {
		return audio.Frame{}, audio.ErrSourceClosed
	}
	samples := s.chunks[s.idx]
	s.idx++
	s.pos.Add(int64(len(samples)))
	return audio.Frame{
		Samples:    samples,
		SampleRate: audio.SampleRate,
		Channels:   1,
		Sequence:   int64(s.idx),
		SourceKind: audio.SourceFile,
		Final:      s.idx == len(s.chunks),
	}, nil
}

func (s *fakeSource) Close() error        { return nil }
func (s *fakeSource) Position() int64     { return s.pos.Load() + s.posBias }
func (s *fakeSource) TotalSamples() int64 { return s.total }

var _ FrameSourceWithPosition = (*fakeSource)(nil)

// fakeDetector releases scripted spans once enough audio has been fed past
// their end, so a test states where speech sits on the timeline without a VAD.
type fakeDetector struct {
	spans    []Span // released when fed reaches their end
	trailing []Span // released by Flush
	pending  []Span
	fed      int64
	fedBias  int64 // added to Fed, to fake a detector that miscounts
}

func (d *fakeDetector) Accept(samples []float32) {
	d.fed += int64(len(samples))
	for len(d.spans) > 0 && d.spans[0].EndSample <= d.fed {
		d.pending = append(d.pending, d.spans[0])
		d.spans = d.spans[1:]
	}
}

func (d *fakeDetector) Next() (Span, bool) {
	if len(d.pending) == 0 {
		return Span{}, false
	}
	span := d.pending[0]
	d.pending = d.pending[1:]
	return span, true
}

func (d *fakeDetector) Flush() {
	d.pending = append(d.pending, d.trailing...)
	d.trailing = nil
}

func (d *fakeDetector) Fed() int64 { return d.fed + d.fedBias }

var _ SpanDetector = (*fakeDetector)(nil)

// fakeDecoder returns scripted text per call.
type fakeDecoder struct {
	texts  []string
	err    error
	onCall func()
	calls  int
}

func (d *fakeDecoder) DecodeSegment(_ context.Context, _ []float32) (string, error) {
	d.calls++
	if d.onCall != nil {
		d.onCall()
	}
	if d.err != nil {
		return "", d.err
	}
	if d.calls-1 < len(d.texts) {
		return d.texts[d.calls-1], nil
	}
	return "", nil
}

var _ stt.SegmentDecoder = (*fakeDecoder)(nil)

// fakeSink records events and can fail or block on a chosen kind.
type fakeSink struct {
	mu      sync.Mutex
	events  []Event
	errOn   string
	err     error
	blockOn string
	release chan struct{}
	blocked chan struct{}
	once    sync.Once
	onEmit  func(Event) // called synchronously from Emit after the event is recorded
}

func (s *fakeSink) Emit(ctx context.Context, ev Event) error {
	s.mu.Lock()
	s.events = append(s.events, ev)
	onEmit := s.onEmit
	s.mu.Unlock()
	if onEmit != nil {
		onEmit(ev)
	}
	if s.blockOn != "" && ev.Kind() == s.blockOn {
		s.once.Do(func() { close(s.blocked) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.errOn != "" && ev.Kind() == s.errOn {
		return s.err
	}
	return nil
}

func (s *fakeSink) kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.events))
	for _, ev := range s.events {
		out = append(out, ev.Kind())
	}
	return out
}

func (s *fakeSink) segments() []Segment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Segment
	for _, ev := range s.events {
		if seg, ok := ev.(Segment); ok {
			out = append(out, seg)
		}
	}
	return out
}

func (s *fakeSink) progress() []Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Progress
	for _, ev := range s.events {
		if p, ok := ev.(Progress); ok {
			out = append(out, p)
		}
	}
	return out
}

var _ Sink = (*fakeSink)(nil)

// silence builds n chunks of 1600 zero samples: 100 ms each, as the file source
// hands them over.
func silence(n int) [][]float32 {
	chunks := make([][]float32, n)
	for i := range chunks {
		chunks[i] = make([]float32, audio.ChunkSize)
	}
	return chunks
}

// speechSpan builds a span whose samples have exactly the requested RMS.
func speechSpan(start, length int64, level float32) Span {
	samples := make([]float32, length)
	for i := range samples {
		if i%2 == 0 {
			samples[i] = level
		} else {
			samples[i] = -level
		}
	}
	return Span{StartSample: start, EndSample: start + length, Samples: samples}
}

func testDeps(source *fakeSource, detector *fakeDetector, decoder *fakeDecoder, sink *fakeSink) Deps {
	return Deps{
		Source:   source,
		Detector: detector,
		Decoder:  decoder,
		Sink:     sink,
		Info: stt.DecoderInfo{
			Provider: "parakeet", Mode: "offline", ModelID: "tdt-0.6b-v2",
			ModelRevision: "sha256:abc", Languages: []string{"en"}, Timing: []string{"utterance"},
		},
	}
}

func requireRunError(t *testing.T, err error) *Error {
	t.Helper()
	runErr, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("Run() error = %v, want *Error", err)
	}
	return runErr
}

func TestRunWithoutSpeechCompletesAsNoSpeech(t *testing.T) {
	source := &fakeSource{chunks: silence(30), total: 48000}
	sink := &fakeSink{}
	deps := testDeps(source, &fakeDetector{}, &fakeDecoder{}, sink)

	summary, err := Run(t.Context(), Request{JobID: "job-1"}, deps)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Outcome != OutcomeNoSpeech || summary.SegmentCount != 0 {
		t.Errorf("summary = %+v, want no_speech with 0 segments", summary)
	}
	if summary.DurationUS != 3_000_000 {
		t.Errorf("DurationUS = %d, want 3000000 for 48000 samples", summary.DurationUS)
	}
	if got, want := strings.Join(sink.kinds(), ","), "accepted,summary"; got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
}

func TestRunReportsTheResolvedDecoderBeforeReadingAudio(t *testing.T) {
	source := &fakeSource{chunks: silence(10), total: 16000}
	readsAtAccepted := int64(-1)
	sink := &fakeSink{
		onEmit: func(ev Event) {
			if _, ok := ev.(Accepted); ok {
				readsAtAccepted = source.reads.Load()
			}
		},
	}
	deps := testDeps(source, &fakeDetector{}, &fakeDecoder{}, sink)

	if _, err := Run(t.Context(), Request{JobID: "job-1", Language: "en"}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if readsAtAccepted != 0 {
		t.Errorf("source reads at Accepted = %d, want 0", readsAtAccepted)
	}
	sink.mu.Lock()
	first := sink.events[0]
	sink.mu.Unlock()
	accepted, ok := first.(Accepted)
	if !ok {
		t.Fatalf("first event = %T, want Accepted", first)
	}
	want := Accepted{
		Provider: "parakeet", Mode: "offline", ModelID: "tdt-0.6b-v2", ModelRevision: "sha256:abc",
		Languages: []string{"en"}, Timing: TimingUtterance, DurationUS: 1_000_000,
	}
	if !reflect.DeepEqual(accepted, want) {
		t.Errorf("accepted = %+v, want %+v", accepted, want)
	}
}

func TestRunKeepsAnEOFSpanInsideTheCeiledFileDuration(t *testing.T) {
	// 16001 is odd, so SamplesToMicros floors and DurationMicros ceils.
	// 2501 would show the same 1 µs gap, but it is shorter than
	// DefaultMinSpeechSamples; this length clears that gate and the
	// speech floor without changing either.
	const total int64 = 16001
	floored := SamplesToMicros(total, audio.SampleRate)
	ceiled := DurationMicros(total, audio.SampleRate)
	if ceiled <= floored {
		t.Fatalf("test setup: %d samples must make floor (%d) and ceil (%d) differ", total, floored, ceiled)
	}

	chunks := silence(10) // 16000 samples
	chunks = append(chunks, make([]float32, 1))
	source := &fakeSource{chunks: chunks, total: total}
	detector := &fakeDetector{trailing: []Span{speechSpan(0, total, 0.05)}}
	sink := &fakeSink{}

	summary, err := Run(t.Context(), Request{JobID: "job-1"}, testDeps(source, detector, &fakeDecoder{texts: []string{"last cue"}}, sink))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.DurationUS != ceiled {
		t.Errorf("Summary.DurationUS = %d, want DurationMicros %d, not SamplesToMicros %d", summary.DurationUS, ceiled, floored)
	}

	sink.mu.Lock()
	first := sink.events[0]
	sink.mu.Unlock()
	accepted, ok := first.(Accepted)
	if !ok {
		t.Fatalf("first event = %T, want Accepted", first)
	}
	if accepted.DurationUS != ceiled {
		t.Errorf("Accepted.DurationUS = %d, want DurationMicros %d", accepted.DurationUS, ceiled)
	}

	segments := sink.segments()
	if len(segments) != 1 {
		t.Fatalf("emitted %d segments, want 1", len(segments))
	}
	seg := segments[0]
	if seg.StartUS >= seg.EndUS {
		t.Errorf("segment spans [%d,%d), which is not a forward interval", seg.StartUS, seg.EndUS)
	}
	if seg.EndUS > summary.DurationUS {
		t.Errorf("segment ends at %d, past Summary.DurationUS %d (the C1 floor/ceil gap)", seg.EndUS, summary.DurationUS)
	}
	if seg.EndUS != ceiled {
		t.Errorf("EOF span EndUS = %d, want the ceiled duration %d", seg.EndUS, ceiled)
	}
}

func TestRunAcceptedLanguagesAreASnapshotOfDecoderInfo(t *testing.T) {
	source := &fakeSource{chunks: silence(10), total: 16000}
	sink := &fakeSink{}
	deps := testDeps(source, &fakeDetector{}, &fakeDecoder{}, sink)
	deps.Info.Languages = []string{"en", "es"}

	if _, err := Run(t.Context(), Request{JobID: "job-1", Language: "fr"}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	sink.mu.Lock()
	first := sink.events[0]
	sink.mu.Unlock()
	accepted, ok := first.(Accepted)
	if !ok {
		t.Fatalf("first event = %T, want Accepted", first)
	}
	want := []string{"en", "es"}
	if !reflect.DeepEqual(accepted.Languages, want) {
		t.Errorf("Accepted.Languages = %v, want the decoder snapshot %v, not Request.Language", accepted.Languages, want)
	}
	if len(accepted.Languages) == 0 {
		t.Fatal("Accepted.Languages is empty")
	}
	accepted.Languages[0] = "mutated"
	if !reflect.DeepEqual(deps.Info.Languages, want) {
		t.Errorf("mutating Accepted.Languages changed Info.Languages to %v", deps.Info.Languages)
	}
}

func TestRunEmitsTrailingSpeechFlushedAtEOF(t *testing.T) {
	// The span ends exactly at EOF, so only Flush can release it.
	source := &fakeSource{chunks: silence(20), total: 32000}
	detector := &fakeDetector{trailing: []Span{speechSpan(16000, 16000, 0.05)}}
	sink := &fakeSink{}
	deps := testDeps(source, detector, &fakeDecoder{texts: []string{"last words"}}, sink)

	summary, err := Run(t.Context(), Request{JobID: "job-1"}, deps)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Outcome != OutcomeSpeech || summary.SegmentCount != 1 {
		t.Fatalf("summary = %+v, want speech with 1 segment", summary)
	}
	segments := sink.segments()
	if len(segments) != 1 {
		t.Fatalf("emitted %d segments, want 1", len(segments))
	}
	want := Segment{ID: "s1", StartUS: 1_000_000, EndUS: 2_000_000, Text: "last words"}
	if segments[0] != want {
		t.Errorf("segment = %+v, want %+v", segments[0], want)
	}
}

func TestRunEmitsMonotonicSegmentIDsAndTimes(t *testing.T) {
	source := &fakeSource{chunks: silence(100), total: 160000}
	detector := &fakeDetector{spans: []Span{
		speechSpan(16000, 16000, 0.05),
		speechSpan(64000, 32000, 0.05),
		speechSpan(112000, 16000, 0.05),
	}}
	decoder := &fakeDecoder{texts: []string{"one", "two", "three"}}
	sink := &fakeSink{}

	summary, err := Run(t.Context(), Request{JobID: "job-1"}, testDeps(source, detector, decoder, sink))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.SegmentCount != 3 {
		t.Fatalf("SegmentCount = %d, want 3", summary.SegmentCount)
	}
	segments := sink.segments()
	wantIDs := []string{"s1", "s2", "s3"}
	for i, seg := range segments {
		if seg.ID != wantIDs[i] {
			t.Errorf("segment %d ID = %q, want %q", i, seg.ID, wantIDs[i])
		}
		if seg.StartUS >= seg.EndUS {
			t.Errorf("segment %s spans [%d,%d), which is not a forward interval", seg.ID, seg.StartUS, seg.EndUS)
		}
		if seg.EndUS > summary.DurationUS {
			t.Errorf("segment %s ends at %d, past the %d duration", seg.ID, seg.EndUS, summary.DurationUS)
		}
		if i > 0 && seg.StartUS < segments[i-1].StartUS {
			t.Errorf("segment %s starts at %d, before %s at %d", seg.ID, seg.StartUS, segments[i-1].ID, segments[i-1].StartUS)
		}
	}
	if segments[0].StartUS != 1_000_000 || segments[2].EndUS != 8_000_000 {
		t.Errorf("segment times = %+v, want the first at 1 s and the last ending at 8 s", segments)
	}
}

func TestRunSkipsSpansThatAreNotWorthDecoding(t *testing.T) {
	tests := []struct {
		name      string
		span      Span
		text      string
		wantCalls int
	}{
		{name: "shorter than the minimum", span: speechSpan(16000, 4000, 0.05), wantCalls: 0},
		{name: "below the speech floor", span: speechSpan(16000, 16000, 0.0001), wantCalls: 0},
		{name: "decodes to nothing", span: speechSpan(16000, 16000, 0.05), text: "   ", wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeSource{chunks: silence(30), total: 48000}
			detector := &fakeDetector{spans: []Span{tt.span}}
			decoder := &fakeDecoder{texts: []string{tt.text}}
			sink := &fakeSink{}

			summary, err := Run(t.Context(), Request{JobID: "job-1"}, testDeps(source, detector, decoder, sink))
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if summary.Outcome != OutcomeNoSpeech || summary.SegmentCount != 0 {
				t.Errorf("summary = %+v, want no_speech with 0 segments", summary)
			}
			if decoder.calls != tt.wantCalls {
				t.Errorf("decoder ran %d times, want %d", decoder.calls, tt.wantCalls)
			}
			if len(sink.segments()) != 0 {
				t.Errorf("emitted %d segments, want none", len(sink.segments()))
			}
		})
	}
}

func TestRunFailsWhenSpansOverlap(t *testing.T) {
	source := &fakeSource{chunks: silence(50), total: 80000}
	detector := &fakeDetector{spans: []Span{
		speechSpan(16000, 16000, 0.05),
		speechSpan(24000, 16000, 0.05), // starts inside the previous span
	}}
	sink := &fakeSink{}

	_, err := Run(t.Context(), Request{JobID: "job-1"}, testDeps(source, detector, &fakeDecoder{texts: []string{"a", "b"}}, sink))
	runErr := requireRunError(t, err)
	if runErr.Code != CodeInternal {
		t.Fatalf("Run() error = %v, want an internal Error", err)
	}
	if !strings.Contains(runErr.Error(), "inside the previous span") {
		t.Errorf("error %q does not explain the overlap", runErr)
	}
}

func TestRunFailsOnDecoderErrorWithoutASummary(t *testing.T) {
	source := &fakeSource{chunks: silence(30), total: 48000}
	detector := &fakeDetector{spans: []Span{speechSpan(16000, 16000, 0.05)}}
	sink := &fakeSink{}
	decoderErr := errors.New("recognizer exploded")
	decoder := &fakeDecoder{err: decoderErr}

	summary, err := Run(t.Context(), Request{JobID: "job-1"}, testDeps(source, detector, decoder, sink))
	runErr := requireRunError(t, err)
	if runErr.Code != CodeDecodeFailed {
		t.Errorf("code = %q, want %q", runErr.Code, CodeDecodeFailed)
	}
	if !strings.Contains(runErr.Error(), "recognizer exploded") {
		t.Errorf("error %q loses the decoder's message", runErr)
	}
	if !errors.Is(err, decoderErr) {
		t.Errorf("errors.Is cannot reach the decoder cause through Unwrap: %v", err)
	}
	if summary != (Summary{}) {
		t.Errorf("summary = %+v, want the zero value on failure", summary)
	}
	if got := strings.Join(sink.kinds(), ","); strings.Contains(got, "summary") {
		t.Errorf("events = %s, want no summary after a failure", got)
	}
}

func TestRunFailsOnSourceErrorNamingTheInput(t *testing.T) {
	source := &fakeSource{
		chunks:  silence(30),
		total:   48000,
		failAt:  3,
		failErr: errors.New("audio: read /tmp/take-1.wav: input/output error"),
	}
	sink := &fakeSink{}

	_, err := Run(t.Context(), Request{JobID: "job-1"}, testDeps(source, &fakeDetector{}, &fakeDecoder{}, sink))
	runErr := requireRunError(t, err)
	if runErr.Code != CodeInputUnreadable {
		t.Errorf("code = %q, want %q", runErr.Code, CodeInputUnreadable)
	}
	if !strings.Contains(runErr.Error(), "/tmp/take-1.wav") {
		t.Errorf("error %q does not name the input", runErr)
	}
}

func TestRunFailsWhenTheSinkFails(t *testing.T) {
	tests := []struct {
		name   string
		errOn  string
		chunks int
		span   bool
	}{
		{name: "accepted", errOn: "accepted", chunks: 30, span: true},
		{name: "progress", errOn: "progress", chunks: 100, span: false},
		{name: "segment", errOn: "segment", chunks: 30, span: true},
		{name: "summary", errOn: "summary", chunks: 30, span: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeSource{chunks: silence(tt.chunks), total: int64(tt.chunks) * int64(audio.ChunkSize)}
			detector := &fakeDetector{}
			if tt.span {
				detector.spans = []Span{speechSpan(16000, 16000, 0.05)}
			}
			sink := &fakeSink{errOn: tt.errOn, err: errors.New("pipe closed")}
			deps := testDeps(source, detector, &fakeDecoder{texts: []string{"hello"}}, sink)

			_, err := Run(t.Context(), Request{JobID: "job-1"}, deps)
			runErr := requireRunError(t, err)
			if runErr.Code != CodeInternal {
				t.Fatalf("Run() error = %v, want an internal Error", err)
			}
			if !strings.Contains(runErr.Error(), "pipe closed") {
				t.Errorf("error %q loses the sink's message", runErr)
			}
		})
	}
}

func TestRunFailsWhenTheTimelinesDisagree(t *testing.T) {
	tests := []struct {
		name  string
		drift func(*fakeSource, *fakeDetector)
	}{
		{
			name:  "the detector was fed more than the runner read",
			drift: func(_ *fakeSource, d *fakeDetector) { d.fedBias = 1600 },
		},
		{
			name:  "the source counted fewer than it handed over",
			drift: func(s *fakeSource, _ *fakeDetector) { s.posBias = -1600 },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeSource{chunks: silence(30), total: 48000}
			detector := &fakeDetector{}
			tt.drift(source, detector)
			sink := &fakeSink{}

			_, err := Run(t.Context(), Request{JobID: "job-1"}, testDeps(source, detector, &fakeDecoder{}, sink))
			runErr := requireRunError(t, err)
			if runErr.Code != CodeInternal {
				t.Fatalf("Run() error = %v, want an internal Error", err)
			}
			if !strings.Contains(runErr.Error(), "timeline drift") {
				t.Errorf("error %q does not name the drift", runErr)
			}
		})
	}
}

func TestRunRejectsIncompleteRequests(t *testing.T) {
	source := &fakeSource{chunks: silence(1), total: 1600}
	tests := []struct {
		name     string
		req      Request
		deps     Deps
		wantCode string
		wantMsg  string
	}{
		{
			name:     "word timing is not produced",
			req:      Request{JobID: "job-1", Timing: "word"},
			deps:     testDeps(source, &fakeDetector{}, &fakeDecoder{}, &fakeSink{}),
			wantCode: CodeUnsupportedCapability,
		},
		{
			name:     "no source",
			req:      Request{JobID: "job-1"},
			deps:     Deps{Detector: &fakeDetector{}, Decoder: &fakeDecoder{}, Sink: &fakeSink{}},
			wantCode: CodeInternal,
		},
		{
			name:     "no sink",
			req:      Request{JobID: "job-1"},
			deps:     Deps{Source: source, Detector: &fakeDetector{}, Decoder: &fakeDecoder{}},
			wantCode: CodeInternal,
		},
		{
			name:     "no detector",
			req:      Request{JobID: "job-1"},
			deps:     Deps{Source: source, Decoder: &fakeDecoder{}, Sink: &fakeSink{}},
			wantCode: CodeInternal,
			wantMsg:  "span detector",
		},
		{
			name:     "no decoder",
			req:      Request{JobID: "job-1"},
			deps:     Deps{Source: source, Detector: &fakeDetector{}, Sink: &fakeSink{}},
			wantCode: CodeInternal,
			wantMsg:  "decoder",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Run(t.Context(), tt.req, tt.deps)
			runErr := requireRunError(t, err)
			if runErr.Code != tt.wantCode {
				t.Fatalf("Run() error = %v, want code %q", err, tt.wantCode)
			}
			if tt.wantMsg != "" && !strings.Contains(runErr.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", runErr, tt.wantMsg)
			}
		})
	}
}

func TestRunThrottlesProgressByConsumedAudio(t *testing.T) {
	// 200 chunks is 20 s. Progress is every 5 s of consumed audio and is not
	// emitted on the final frame, so the events land at 5 s, 10 s and 15 s.
	source := &fakeSource{chunks: silence(200), total: 320000}
	sink := &fakeSink{}

	summary, err := Run(t.Context(), Request{JobID: "job-1"}, testDeps(source, &fakeDetector{}, &fakeDecoder{}, sink))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Outcome != OutcomeNoSpeech {
		t.Errorf("summary = %+v, want no_speech", summary)
	}
	got := sink.progress()
	want := []Progress{
		{ConsumedUS: 5_000_000, DurationUS: 20_000_000},
		{ConsumedUS: 10_000_000, DurationUS: 20_000_000},
		{ConsumedUS: 15_000_000, DurationUS: 20_000_000},
	}
	if len(got) != len(want) {
		t.Fatalf("progress events = %d, want %d (throttled, not one per frame)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("progress[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got, want := strings.Join(sink.kinds(), ","), "accepted,progress,progress,progress,summary"; got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
}

func TestRunReturnsContextCanceledWhenDecodeIsInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := &fakeSource{chunks: silence(30), total: 48000}
	detector := &fakeDetector{spans: []Span{speechSpan(16000, 16000, 0.05)}}
	decoder := &fakeDecoder{onCall: cancel, err: errors.New("decode interrupted")}
	sink := &fakeSink{}

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Request{JobID: "job-1"}, testDeps(source, detector, decoder, sink))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestRunStopsPromptlyOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	source := &fakeSource{chunks: silence(200), total: 320000}
	sink := &fakeSink{}

	_, err := Run(ctx, Request{JobID: "job-1"}, testDeps(source, &fakeDetector{}, &fakeDecoder{}, sink))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if source.reads.Load() != 0 {
		t.Errorf("read the source %d times after cancellation", source.reads.Load())
	}
}

func TestRunStopsReadingWhileTheSinkBlocks(t *testing.T) {
	source := &fakeSource{chunks: silence(200), total: 320000}
	detector := &fakeDetector{spans: []Span{speechSpan(0, 16000, 0.05)}}
	sink := &fakeSink{blockOn: "segment", release: make(chan struct{}), blocked: make(chan struct{})}
	deps := testDeps(source, detector, &fakeDecoder{texts: []string{"hello"}}, sink)

	done := make(chan error, 1)
	go func() {
		_, err := Run(t.Context(), Request{JobID: "job-1"}, deps)
		done <- err
	}()

	select {
	case <-sink.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the sink was never offered a segment")
	}
	held := source.reads.Load()
	time.Sleep(50 * time.Millisecond)
	if got := source.reads.Load(); got != held {
		t.Fatalf("the source was read %d more times while the sink was blocked; audio must wait for the consumer", got-held)
	}
	if held >= int64(len(source.chunks)) {
		t.Fatalf("the source was fully drained (%d reads) before the sink blocked", held)
	}
	close(sink.release)
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := source.reads.Load(); got <= held {
		t.Errorf("reads stayed at %d after the sink was released", got)
	}
}
