// Package transcript appends conversation events to a JSONL file — the
// machine-readable session record consumed by the duet harness and useful on
// its own for field debugging (WI-dc9e33 PR-1). One JSON object per line; the
// first line is a header carrying the schema version.
package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// queueCapacity bounds the writer queue. Bus handlers run synchronously on
// the emitting pipeline goroutine, so a slow disk must drop records rather
// than stall a turn; drops are counted and reported in the final record.
const queueCapacity = 1024

// summaryPreviewMax caps ConversationCompacted / SpeakingStarted text so a
// single line cannot dominate the log.
const summaryPreviewMax = 240

// Version is the JSONL schema version written in the header record.
//
// 2 added per-segment TTS cost to the metrics record (segments, tts_provider,
// tts_tier, tts_fallbacks, first_audio_grace_s, stall_watchdog_fired). All are
// additive and omitted when zero, so a v1 reader still parses a v2 line.
const Version = 2

// record is the flat wire form for every line. Zero fields are omitted, so
// each type carries only its own keys.
type record struct {
	WorkerUsage  *events.WorkerUsageRecorded `json:"worker_usage,omitempty"`
	WorkerLimit  *events.WorkerLimitReached  `json:"worker_limit,omitempty"`
	WorkerCharge *events.WorkerChargeState   `json:"worker_charge,omitempty"`
	Delivery     *events.WorkDelivery        `json:"delivery,omitempty"`
	Work         *work.Snapshot              `json:"work,omitempty"`
	WorkScope    *work.WorkScope             `json:"work_scope,omitempty"`
	Type         string                      `json:"type"`
	TS           string                      `json:"ts,omitempty"`
	Seq          int64                       `json:"seq,omitempty"`

	V   int `json:"v,omitempty"`   // header
	PID int `json:"pid,omitempty"` // header

	Text         string   `json:"text,omitempty"`
	Name         string   `json:"name,omitempty"`
	ID           string   `json:"id,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	Tier         string   `json:"tier,omitempty"`
	ReasonCode   string   `json:"reason_code,omitempty"`
	JobID        string   `json:"job_id,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Preview      string   `json:"preview,omitempty"`
	Stage        string   `json:"stage,omitempty"`
	Message      string   `json:"message,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	State        string   `json:"state,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Outcome      string   `json:"outcome,omitempty"`
	Symptom      string   `json:"symptom,omitempty"`
	Cause        string   `json:"cause,omitempty"`
	Confidence   string   `json:"confidence,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Scope        string   `json:"scope,omitempty"`
	ActionCode   string   `json:"action_code,omitempty"`
	DedupeKey    string   `json:"dedupe_key,omitempty"`
	EngineRTF    float64  `json:"engine_rtf,omitempty"`
	GapS         float64  `json:"gap_s,omitempty"`
	SampleCount  int      `json:"sample_count,omitempty"`
	AudioSeconds float64  `json:"audio_seconds,omitempty"`
	Exclusions   []string `json:"exclusions,omitempty"`
	Underruns    int      `json:"underruns,omitempty"`
	Degraded     bool     `json:"degraded,omitempty"`
	Interrupted  bool     `json:"interrupted,omitempty"`
	IsError      bool     `json:"is_error,omitempty"`

	STTFinalS      float64 `json:"stt_s,omitempty"`
	ModelFirstS    float64 `json:"model_first_s,omitempty"`
	ModelS         float64 `json:"model_s,omitempty"`
	VoiceS         float64 `json:"voice_s,omitempty"`
	PlaybackStartS float64 `json:"playback_start_s,omitempty"`
	// SpokeS on metrics is T_eou-to-local-Done (PlaybackCompleteElapsed).
	// Zero when AudibleSink is stream_client or none — fanout drain is not
	// playback. Per-segment speaking.complete records still use PlayElapsed.
	SpokeS   float64 `json:"spoke_s,omitempty"`
	BargeInS float64 `json:"barge_in_s,omitempty"`

	// ElapsedS is one tool call's or one job's wall time.
	ElapsedS float64 `json:"elapsed_s,omitempty"`
	// Tool outcome facts are additive to the legacy preview/message fields.
	Status       string `json:"status,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	HTTPStatus   *int   `json:"http_status,omitempty"`
	Signal       string `json:"signal,omitempty"`
	OutputRef    string `json:"output_ref,omitempty"`
	OutputBytes  int64  `json:"output_bytes,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	InputSummary string `json:"input_summary,omitempty"`

	Prefill   int `json:"prefill,omitempty"`
	Gen       int `json:"gen,omitempty"`
	Threshold int `json:"threshold,omitempty"`

	TurnsBefore int   `json:"turns_before,omitempty"`
	Dropped     int64 `json:"dropped,omitempty"`
	WriteErrors int64 `json:"write_errors,omitempty"`

	// LeakLines is the voice gate's stripped-line count: per event on
	// voice_gate records, per turn on metrics records (WI-dc9e33 B4).
	LeakLines int `json:"leak_lines,omitempty"`

	// TTS accounting on metrics records. The milestone fields above only ever
	// describe the first segment; Segments carries every segment's synthesis
	// cost, which is where a long reply actually spends its time.
	TTSProvider string `json:"tts_provider,omitempty"`
	TTSTier     string `json:"tts_tier,omitempty"`
	// TTSPlace and TTSStreaming are the turn's topology: which computer
	// synthesized, and whether its worker emitted PCM during decode. Without
	// them a slow turn in this log cannot be told from a fast one that
	// crossed a LAN.
	TTSPlace           string    `json:"tts_place,omitempty"`
	TTSStreaming       bool      `json:"tts_streaming,omitempty"`
	TTSWarm            bool      `json:"tts_warm,omitempty"`
	TTSFallbacks       int       `json:"tts_fallbacks,omitempty"`
	FirstAudioGraceS   float64   `json:"first_audio_grace_s,omitempty"`
	StallWatchdogFired bool      `json:"stall_watchdog_fired,omitempty"`
	DeadAirS           float64   `json:"dead_air_s,omitempty"`
	Segments           []segment `json:"segments,omitempty"`
}

// segment is one synthesized speech segment's cost. rtf is carried rather
// than left to the reader because it is the quantity every consumer wants and
// the one an aggregate must not recompute differently.
type segment struct {
	Index int `json:"i"`
	Chars int `json:"chars,omitempty"`
	// EngineS is synthesis alone; SynthS also covers waiting behind whatever
	// was already playing. Only the first segment's SynthS is a delay a
	// listener experienced.
	EngineS          float64  `json:"engine_s"`
	SynthS           float64  `json:"synth_s"`
	QueueS           float64  `json:"queue_s,omitempty"`
	AudioS           float64  `json:"audio_s"`
	RTF              float64  `json:"rtf,omitempty"`
	PlayS            float64  `json:"play_s,omitempty"`
	Fallback         bool     `json:"fallback,omitempty"`
	Interrupted      bool     `json:"interrupted,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	Tier             string   `json:"tier,omitempty"`
	Place            string   `json:"place,omitempty"`
	Streaming        bool     `json:"streaming,omitempty"`
	TextReadyS       float64  `json:"text_ready_s,omitempty"`
	WarmState        string   `json:"warm_state,omitempty"`
	Error            string   `json:"error,omitempty"`
	Exclusions       []string `json:"exclusions,omitempty"`
	WholeSentence    bool     `json:"whole_sentence_playback"`
	Underruns        int      `json:"underruns,omitempty"`
	GapS             float64  `json:"gap_s,omitempty"`
	GapCause         string   `json:"gap_cause,omitempty"`
	Gaps             []gap    `json:"gaps,omitempty"`
	SynthQueuedS     float64  `json:"synth_queued_s,omitempty"`
	SynthStartedS    float64  `json:"synth_started_s,omitempty"`
	SynthFirstPCMS   float64  `json:"synth_first_pcm_s,omitempty"`
	SynthEndedS      float64  `json:"synth_ended_s,omitempty"`
	PlaybackStartedS float64  `json:"playback_started_s,omitempty"`
	PlaybackEndedS   float64  `json:"playback_ended_s,omitempty"`
}

type gap struct {
	StartS float64 `json:"start_s"`
	EndS   float64 `json:"end_s"`
	Cause  string  `json:"cause,omitempty"`
}

// Writer subscribes to a bus and appends one record per event.
//
// Concurrency: enqueue and Close are serialized on mu so a late bus Emit after
// Close never panics with "send on closed channel". The writer goroutine alone
// owns the file after Open.
type Writer struct {
	f           *os.File
	ch          chan record
	done        chan struct{}
	seq         atomic.Int64
	dropped     atomic.Int64
	writeErrors atomic.Int64
	mu          sync.Mutex // protects closed + channel send/rebuild
	closed      bool
	once        sync.Once
	warnedIO    atomic.Bool
}

// NewWriter opens (appending) the JSONL file and starts the writer goroutine.
func NewWriter(path string) (*Writer, error) {
	return newWriter(path, queueCapacity)
}

func newWriter(path string, capacity int) (*Writer, error) {
	// Conversation content may include secrets; match diagnostics log perms.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open transcript log: %w", err)
	}
	w := &Writer{f: f, ch: make(chan record, capacity), done: make(chan struct{})}
	go w.run()
	w.enqueue(record{Type: "header", V: Version, PID: os.Getpid()})
	return w, nil
}

// Attach subscribes the writer to the events the transcript records.
// High-rate display traffic (AudioLevel, TranscriptPartial, per-segment voice
// events) stays excluded by design — per-segment synthesis cost rides the
// once-per-turn metrics record instead, so a long reply still costs one line. agent_delta is recorded but non-durable
// under back-pressure so terminal user/agent/error/metrics lines win.
func (w *Writer) Attach(bus *events.Bus) {
	if bus == nil {
		return
	}
	// Typed subscriptions so AudioLevel / TranscriptPartial never enter the
	// writer. --transcript-log is process-lifetime; /record uses AttachAll.
	events.Subscribe(bus, func(e events.UserInput) { w.Handle(e) })
	events.Subscribe(bus, func(e events.ResponseDelta) { w.Handle(e) })
	events.Subscribe(bus, func(e events.ResponseReady) { w.Handle(e) })
	events.Subscribe(bus, func(e events.ToolCallStarted) { w.Handle(e) })
	events.Subscribe(bus, func(e events.ToolCallFinished) { w.Handle(e) })
	events.Subscribe(bus, func(e events.WorkStateChanged) { w.Handle(e) })
	events.Subscribe(bus, func(e events.WorkerUsageRecorded) { w.Handle(e) })
	events.Subscribe(bus, func(e events.WorkerLimitReached) { w.Handle(e) })
	events.Subscribe(bus, func(e events.WorkerChargeState) { w.Handle(e) })
	events.Subscribe(bus, func(e events.RouteDecided) { w.Handle(e) })
	events.Subscribe(bus, func(e events.WorkDelivery) { w.Handle(e) })
	events.Subscribe(bus, func(e events.JobQueued) { w.Handle(e) })
	events.Subscribe(bus, func(e events.JobStarted) { w.Handle(e) })
	events.Subscribe(bus, func(e events.JobFinished) { w.Handle(e) })
	events.Subscribe(bus, func(e events.JobFailed) { w.Handle(e) })
	events.Subscribe(bus, func(e events.JobAborted) { w.Handle(e) })
	events.Subscribe(bus, func(e events.Error) { w.Handle(e) })
	events.Subscribe(bus, func(e events.Info) { w.Handle(e) })
	events.Subscribe(bus, func(e events.SpeechDiagnostic) { w.Handle(e) })
	events.Subscribe(bus, func(e events.STTPhase) { w.Handle(e) })
	events.Subscribe(bus, func(e events.ThinkingStarted) { w.Handle(e) })
	events.Subscribe(bus, func(e events.SpeakingStarted) { w.Handle(e) })
	events.Subscribe(bus, func(e events.SpeakingComplete) { w.Handle(e) })
	events.Subscribe(bus, func(e events.SpeakingInterrupted) { w.Handle(e) })
	events.Subscribe(bus, func(e events.TurnInterrupted) { w.Handle(e) })
	events.Subscribe(bus, func(e events.TurnMetrics) { w.Handle(e) })
	events.Subscribe(bus, func(e events.VoiceGateStripped) { w.Handle(e) })
	events.Subscribe(bus, func(e events.TokenUsage) { w.Handle(e) })
	events.Subscribe(bus, func(e events.SessionWarning) { w.Handle(e) })
	events.Subscribe(bus, func(e events.ConversationCompacted) { w.Handle(e) })
	events.Subscribe(bus, func(e events.ConversationCleared) { w.Handle(e) })
}

// segmentRecords converts the turn's segment metrics to their wire form.
func segmentRecords(in []events.SegmentMetric) []segment {
	if len(in) == 0 {
		return nil
	}
	out := make([]segment, 0, len(in))
	for _, s := range in {
		out = append(out, segment{
			Index:            s.Index,
			Chars:            s.Chars,
			EngineS:          s.EngineElapsed.Seconds(),
			QueueS:           s.QueueElapsed().Seconds(),
			SynthS:           s.SynthElapsed.Seconds(),
			AudioS:           s.AudioSeconds,
			RTF:              s.RealtimeFactor(),
			PlayS:            s.PlayElapsed.Seconds(),
			Fallback:         s.Fallback,
			Interrupted:      s.Interrupted,
			Provider:         s.Provider,
			Tier:             s.Tier,
			Place:            s.Place,
			Streaming:        s.WorkerStreaming,
			TextReadyS:       s.TextReadyElapsed.Seconds(),
			WarmState:        s.WarmState,
			Error:            s.Error,
			Exclusions:       append([]string(nil), s.Exclusions...),
			WholeSentence:    s.WholeSentencePlayback,
			Underruns:        s.Underruns,
			GapS:             s.GapElapsed.Seconds(),
			GapCause:         s.GapCause,
			Gaps:             speechGaps(s.Gaps),
			SynthQueuedS:     s.SynthQueuedElapsed.Seconds(),
			SynthStartedS:    s.SynthStartedElapsed.Seconds(),
			SynthFirstPCMS:   s.SynthFirstPCMElapsed.Seconds(),
			SynthEndedS:      s.SynthEndedElapsed.Seconds(),
			PlaybackStartedS: s.PlaybackStartedElapsed.Seconds(),
			PlaybackEndedS:   s.PlaybackEndedElapsed.Seconds(),
		})
	}
	return out
}

func speechGaps(in []events.SpeechGap) []gap {
	if len(in) == 0 {
		return nil
	}
	out := make([]gap, 0, len(in))
	for _, g := range in {
		out = append(out, gap{StartS: g.StartElapsed.Seconds(), EndS: g.EndElapsed.Seconds(), Cause: g.Cause})
	}
	return out
}

// isDurableRecord reports terminal / harness-critical record types that must
// outrank streaming noise under back-pressure (same idea as the TUI bridge).
func isDurableRecord(r record) bool {
	switch r.Type {
	case "header", "user", "agent", "error", "tool_call", "tool_result",
		"metrics", "tokens", "session_warning", "compacted", "cleared",
		"turn_interrupted", "work_state_changed", "work_delivery", "dropped":
		return true
	default:
		return false
	}
}

// enqueue stamps and queues one record; it never blocks the emitting
// goroutine. The bus outlives conversations, so post-Close events are dropped
// silently rather than written to a closed file.
func (w *Writer) enqueue(r record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	r.Seq = w.seq.Add(1)

	select {
	case w.ch <- r:
		return
	default:
	}

	// Channel full: prefer durable records over agent_delta / speaking / etc.
	// Mirror the TUI bridge — drop oldest non-durable first, then oldest durable.
	q := make([]record, 0, cap(w.ch)+1)
	for {
		select {
		case m := <-w.ch:
			q = append(q, m)
		default:
			goto drained
		}
	}
drained:
	q = append(q, r)
	before := len(q)
	q = fitTranscriptQueue(q, cap(w.ch))
	if dropped := before - len(q); dropped > 0 {
		w.dropped.Add(int64(dropped))
	}
	for _, m := range q {
		select {
		case w.ch <- m:
		default:
			// Should be impossible after fit; count rather than block.
			w.dropped.Add(1)
		}
	}
}

// fitTranscriptQueue keeps at most capacity records, dropping oldest
// non-durable first. If only durable events remain, the oldest durable drops.
func fitTranscriptQueue(q []record, capacity int) []record {
	if capacity <= 0 {
		return nil
	}
	for len(q) > capacity {
		drop := -1
		for i, m := range q {
			if !isDurableRecord(m) {
				drop = i
				break
			}
		}
		if drop < 0 {
			q = q[1:]
			continue
		}
		q = append(q[:drop], q[drop+1:]...)
	}
	return q
}

func (w *Writer) run() {
	defer close(w.done)
	for r := range w.ch {
		w.write(r)
	}
	if n := w.dropped.Load(); n > 0 || w.writeErrors.Load() > 0 {
		w.write(record{
			Type:        "dropped",
			TS:          time.Now().UTC().Format(time.RFC3339Nano),
			Seq:         w.seq.Add(1),
			Dropped:     n,
			WriteErrors: w.writeErrors.Load(),
		})
	}
}

// write appends and fsyncs one line. Event rate is human-conversation scale,
// so per-record durability is affordable; a torn final line after a crash is
// tolerated by readers. I/O failures are counted and warned once on stderr so
// a harness is not silently incomplete mid-run.
func (w *Writer) write(r record) {
	data, err := json.Marshal(r)
	if err != nil {
		w.noteWriteError(fmt.Errorf("marshal: %w", err))
		return
	}
	if _, err := w.f.Write(append(data, '\n')); err != nil {
		w.noteWriteError(fmt.Errorf("write: %w", err))
		return
	}
	if err := w.f.Sync(); err != nil {
		w.noteWriteError(fmt.Errorf("sync: %w", err))
	}
}

func (w *Writer) noteWriteError(err error) {
	w.writeErrors.Add(1)
	if w.warnedIO.CompareAndSwap(false, true) {
		fmt.Fprintf(os.Stderr, "%s obey-voice: transcript log write failed (further I/O errors counted, not repeated): %v\n",
			time.Now().UTC().Format(time.RFC3339), err)
	}
}

// Close stops accepting events, drains the queue, records drops, and closes
// the file. Safe to call more than once.
func (w *Writer) Close() error {
	var err error
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.ch)
		w.mu.Unlock()
		<-w.done
		err = w.f.Close()
	})
	return err
}

// Dropped is the count of records discarded under back-pressure.
func (w *Writer) Dropped() int64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

func previewText(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
