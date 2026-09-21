package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Event types.
const (
	TypeAccepted  = "accepted"
	TypeProgress  = "progress"
	TypeSegment   = "segment"
	TypeCompleted = "completed"
	TypeFailed    = "failed"
	TypeCancelled = "cancelled"
)

// Outcomes reported by completed.
const (
	OutcomeSpeech   = "speech"
	OutcomeNoSpeech = "no_speech"
)

// MaxLineBytes caps one JSONL line, newline included. A client rejects a
// longer line instead of buffering it.
const MaxLineBytes = 1 << 20

// Client-side stream errors. They describe a stream a reader refuses, not a
// job failure, so they carry no wire code.
var (
	ErrNoTerminal    = errors.New("protocol: stream ended without a terminal event")
	ErrLineTooLong   = errors.New("protocol: event line is over the line cap")
	ErrAfterTerminal = errors.New("protocol: event after the terminal event")
)

// envelope is the four fields every event carries. It is unexported and
// embedded by value, which both keeps the encoder the only thing that fills it
// and seals the Event interface.
type envelope struct {
	Protocol string `json:"protocol"`
	JobID    string `json:"job_id"`
	Seq      int64  `json:"seq"`
	Kind     string `json:"type"`
}

func (e *envelope) stamp(protocol, jobID string, seq int64) {
	e.Protocol, e.JobID, e.Seq = protocol, jobID, seq
}

// Type reports the event's type field.
func (e envelope) Type() string { return e.Kind }

// JobIDOf reports the job the event belongs to.
func (e envelope) JobIDOf() string { return e.JobID }

// SeqOf reports the event's sequence number.
func (e envelope) SeqOf() int64 { return e.Seq }

// Event is one line of the stream. The interface is sealed: stamp is
// unexported, so the wire types are exactly the ones defined here.
type Event interface {
	Type() string
	JobIDOf() string
	SeqOf() int64
	stamp(protocol, jobID string, seq int64)
}

// Accepted reports the resolved backend. It never names the preference string
// the request asked for.
type Accepted struct {
	envelope
	Provider      string `json:"provider"`
	ModelID       string `json:"model_id"`
	ModelRevision string `json:"model_revision"`
	Timing        string `json:"timing"`
}

// NewAccepted builds the accepted event for a resolved decoder.
func NewAccepted(provider, modelID, modelRevision, timing string) *Accepted {
	return &Accepted{
		envelope:      envelope{Kind: TypeAccepted},
		Provider:      provider,
		ModelID:       modelID,
		ModelRevision: modelRevision,
		Timing:        timing,
	}
}

// Progress reports consumed audio against the known duration. Both are real
// measurements: v1 never reports a percentage it cannot compute.
type Progress struct {
	envelope
	ConsumedUS int64 `json:"consumed_us"`
	DurationUS int64 `json:"duration_us"`
}

// NewProgress builds a progress event.
func NewProgress(consumedUS, durationUS int64) *Progress {
	return &Progress{envelope: envelope{Kind: TypeProgress}, ConsumedUS: consumedUS, DurationUS: durationUS}
}

// Segment is one committed utterance. v1 has no partial or revision events, so
// Final is always true.
type Segment struct {
	envelope
	SegmentID string `json:"segment_id"`
	StartUS   int64  `json:"start_us"`
	EndUS     int64  `json:"end_us"`
	Text      string `json:"text"`
	Timing    string `json:"timing"`
	Final     bool   `json:"final"`
}

// NewSegment builds a committed segment event.
func NewSegment(segmentID string, startUS, endUS int64, text, timing string) *Segment {
	return &Segment{
		envelope:  envelope{Kind: TypeSegment},
		SegmentID: segmentID,
		StartUS:   startUS,
		EndUS:     endUS,
		Text:      text,
		Timing:    timing,
		Final:     true,
	}
}

// Completed is the success terminal event. No speech is a success with
// SegmentCount 0 and OutcomeNoSpeech.
type Completed struct {
	envelope
	DurationUS   int64  `json:"duration_us"`
	SegmentCount int    `json:"segment_count"`
	Outcome      string `json:"outcome"`
}

// NewCompleted builds the success terminal event.
func NewCompleted(durationUS int64, segmentCount int, outcome string) *Completed {
	return &Completed{
		envelope:     envelope{Kind: TypeCompleted},
		DurationUS:   durationUS,
		SegmentCount: segmentCount,
		Outcome:      outcome,
	}
}

// Failed is the failure terminal event. Segments already emitted stay partial:
// a failed stream never implies success.
type Failed struct {
	envelope
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// NewFailed builds the failure terminal event.
func NewFailed(code, message string, retryable bool) *Failed {
	return &Failed{envelope: envelope{Kind: TypeFailed}, Code: code, Message: message, Retryable: retryable}
}

// NewFailedFor builds the failure terminal event for err. An error that is not
// an *Error is reported as internal, because a code a client cannot branch on
// is worse than one that says "this build is at fault".
func NewFailedFor(err error) *Failed {
	if err == nil {
		return NewFailed(CodeInternal, "unspecified error", false)
	}
	if coded, ok := errors.AsType[*Error](err); ok {
		if coded == nil {
			return NewFailed(CodeInternal, "unspecified error", false)
		}
		message := ""
		if coded.Err != nil {
			message = coded.Err.Error()
		} else if coded.Code != "" {
			message = coded.Code
		} else {
			message = "unspecified error"
		}
		return NewFailed(coded.Code, message, coded.Retryable)
	}
	return NewFailed(CodeInternal, err.Error(), false)
}

// Cancelled is the terminal event for an interrupted job.
type Cancelled struct {
	envelope
}

// NewCancelled builds the cancellation terminal event.
func NewCancelled() *Cancelled { return &Cancelled{envelope: envelope{Kind: TypeCancelled}} }

var (
	_ Event = (*Accepted)(nil)
	_ Event = (*Progress)(nil)
	_ Event = (*Segment)(nil)
	_ Event = (*Completed)(nil)
	_ Event = (*Failed)(nil)
	_ Event = (*Cancelled)(nil)
)

// IsTerminal reports whether an event type ends the stream.
func IsTerminal(kind string) bool {
	switch kind {
	case TypeCompleted, TypeFailed, TypeCancelled:
		return true
	}
	return false
}

func nilEvent(ev Event) bool {
	if ev == nil {
		return true
	}
	switch e := ev.(type) {
	case *Accepted:
		return e == nil
	case *Progress:
		return e == nil
	case *Segment:
		return e == nil
	case *Completed:
		return e == nil
	case *Failed:
		return e == nil
	case *Cancelled:
		return e == nil
	default:
		return false
	}
}

// Encoder writes the event stream. It stamps the protocol string, job ID and
// sequence number, allows exactly one terminal event, and holds the line cap.
//
// A write that emits any bytes and then fails fences the encoder: later
// Encode calls return that error and do not write again. An overflow whose
// substitute also cannot fit fences the same way without writing. Terminal
// reports only a terminal event that was fully written.
//
// One goroutine at a time: the command writes events from the runner's
// goroutine and nothing else writes to stdout.
type Encoder struct {
	w        io.Writer
	jobID    string
	seq      int64
	terminal bool
	writeErr error
}

// NewEncoder returns an encoder writing jobID's stream to w.
func NewEncoder(w io.Writer, jobID string) *Encoder {
	return &Encoder{w: w, jobID: jobID}
}

// Terminal reports whether the terminal event has been written.
func (e *Encoder) Terminal() bool { return e.terminal }

// Encode stamps ev and writes it as one line.
//
// An event whose line would exceed the cap is replaced by a failed event with
// code internal, which is written instead and ends the stream; Encode then
// returns that *Error so the caller exits on it rather than emitting a second
// terminal event. If even that substitute cannot fit, Encode returns the
// *Error, fences later writes with it, and leaves Terminal false because
// nothing was written.
func (e *Encoder) Encode(ev Event) error {
	if e.writeErr != nil {
		return e.writeErr
	}
	if e.terminal {
		return ErrAfterTerminal
	}
	if nilEvent(ev) {
		return errors.New("protocol: nil event")
	}
	line, err := e.marshal(ev)
	if err != nil {
		return err
	}

	var overflow *Error
	if len(line)+1 > MaxLineBytes {
		overflow = Errorf(CodeInternal, false,
			"%s event is %d bytes, over the %d byte line cap", ev.Type(), len(line)+1, MaxLineBytes)
		ev, line, err = e.boundedFailed(overflow.Err.Error())
		if err != nil {
			return err
		}
		if line == nil {
			// The substitute itself cannot fit. Do not write past the cap
			// and do not report a terminal that was never written. Fence
			// later Encode calls with the overflow so the caller cannot
			// emit another line.
			e.writeErr = overflow
			return overflow
		}
	}

	if err := e.writeLine(line, ev.Type()); err != nil {
		return err
	}
	e.seq++
	if IsTerminal(ev.Type()) {
		e.terminal = true
	}
	if overflow != nil {
		return overflow
	}
	return nil
}

func (e *Encoder) boundedFailed(message string) (Event, []byte, error) {
	for _, msg := range []string{message, "event exceeds line cap"} {
		ev := NewFailed(CodeInternal, msg, false)
		line, err := e.marshal(ev)
		if err != nil {
			return nil, nil, err
		}
		if len(line)+1 <= MaxLineBytes {
			return ev, line, nil
		}
	}
	return nil, nil, nil
}

func (e *Encoder) writeLine(line []byte, kind string) error {
	line = append(line, '\n')
	n, err := e.w.Write(line)
	if n == len(line) && err == nil {
		return nil
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	wrapped := fmt.Errorf("protocol: write %s event: %w", kind, err)
	if n > 0 {
		e.writeErr = wrapped
	}
	return wrapped
}

func (e *Encoder) marshal(ev Event) ([]byte, error) {
	ev.stamp(Protocol, e.jobID, e.seq)
	line, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("protocol: encode %s event: %w", ev.Type(), err)
	}
	return line, nil
}

// Decoder reads an event stream and enforces the client rules: the protocol
// string, the job ID, sequence numbers consecutive from 0, sane segment
// intervals, at most one terminal event, and the line cap.
type Decoder struct {
	r         *bufio.Reader
	jobID     string
	next      int64
	terminal  string
	lastStart int64
	maxEndUS  int64
	segments  int
}

// NewDecoder reads jobID's stream from r. An empty jobID adopts the ID the
// first event carries.
func NewDecoder(r io.Reader, jobID string) *Decoder {
	// ReadSlice reports bufio.ErrBufferFull when a line does not fit. The
	// buffer size is the cap including the newline, so a MaxLineBytes line
	// (JSON + '\n') is accepted and one extra byte is not. Scanner's token
	// limit excludes the delimiter, so the decoder does not use bufio.Scanner.
	//
	// NewReaderSize would reuse a *bufio.Reader whose buffer is already large
	// enough and silently raise the cap; the wrapper forces our own buffer.
	return &Decoder{
		r:     bufio.NewReaderSize(struct{ io.Reader }{r}, MaxLineBytes),
		jobID: jobID,
	}
}

// Next returns the next event, io.EOF at a stream that ended after its
// terminal event, and ErrNoTerminal at one that ended before it.
func (d *Decoder) Next() (Event, error) {
	line, err := d.readLine()
	if err != nil {
		if errors.Is(err, io.EOF) {
			if d.terminal == "" {
				return nil, ErrNoTerminal
			}
			return nil, io.EOF
		}
		return nil, err
	}
	if d.terminal != "" {
		return nil, fmt.Errorf("%w: %s followed %s", ErrAfterTerminal, string(line), d.terminal)
	}

	var head struct {
		Protocol string `json:"protocol"`
		JobID    string `json:"job_id"`
		Seq      *int64 `json:"seq"`
		Kind     string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, fmt.Errorf("protocol: parse event: %w", err)
	}
	if head.Protocol != Protocol {
		return nil, fmt.Errorf("protocol: event protocol %q is not %q", head.Protocol, Protocol)
	}
	if head.JobID == "" {
		return nil, errors.New("protocol: event has no job_id")
	}
	if d.jobID != "" && head.JobID != d.jobID {
		return nil, fmt.Errorf("protocol: event job_id %q is not %q", head.JobID, d.jobID)
	}
	if head.Seq == nil {
		return nil, errors.New("protocol: event has no seq")
	}
	if *head.Seq != d.next {
		return nil, fmt.Errorf("protocol: event seq %d is not %d", *head.Seq, d.next)
	}

	ev, err := decodeEvent(head.Kind, line)
	if err != nil {
		return nil, err
	}
	if err := d.check(ev); err != nil {
		return nil, err
	}

	if d.jobID == "" {
		d.jobID = head.JobID
	}
	d.next++
	if IsTerminal(ev.Type()) {
		d.terminal = ev.Type()
	}
	return ev, nil
}

func (d *Decoder) readLine() ([]byte, error) {
	line, err := d.r.ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return nil, ErrLineTooLong
	case errors.Is(err, io.EOF):
		if len(line) == 0 {
			return nil, io.EOF
		}
		// Last line with no trailing newline.
	case err != nil:
		return nil, fmt.Errorf("protocol: read event stream: %w", err)
	}
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	// Encoder refuses len(json)+1 > MaxLineBytes. A last line without a
	// newline that fills the buffer is the same overflow, just missing '\n'.
	if len(line)+1 > MaxLineBytes {
		return nil, ErrLineTooLong
	}
	if len(line) == 0 {
		return nil, errors.New("protocol: blank line in the event stream")
	}
	return line, nil
}

func decodeEvent(kind string, line []byte) (Event, error) {
	var ev Event
	switch kind {
	case TypeAccepted:
		ev = new(Accepted)
	case TypeProgress:
		ev = new(Progress)
	case TypeSegment:
		ev = new(Segment)
	case TypeCompleted:
		ev = new(Completed)
	case TypeFailed:
		ev = new(Failed)
	case TypeCancelled:
		ev = new(Cancelled)
	default:
		return nil, fmt.Errorf("protocol: unknown event type %q", kind)
	}
	if err := json.Unmarshal(line, ev); err != nil {
		return nil, fmt.Errorf("protocol: parse %s event: %w", kind, err)
	}
	return ev, nil
}

// check enforces the rules that need stream state: intervals inside a segment,
// segment order, and a duration that covers every segment. It mutates decoder
// segment state only after the event itself is valid.
func (d *Decoder) check(ev Event) error {
	switch e := ev.(type) {
	case *Segment:
		switch {
		case e.StartUS < 0:
			return fmt.Errorf("protocol: segment %s starts at %d us", e.SegmentID, e.StartUS)
		case e.EndUS <= e.StartUS:
			return fmt.Errorf("protocol: segment %s spans %d..%d us", e.SegmentID, e.StartUS, e.EndUS)
		case e.StartUS < d.lastStart:
			return fmt.Errorf("protocol: segment %s starts at %d us, before the previous segment's %d us", e.SegmentID, e.StartUS, d.lastStart)
		case e.SegmentID == "":
			return errors.New("protocol: segment has no segment_id")
		case !e.Final:
			return fmt.Errorf("protocol: segment %s is not final", e.SegmentID)
		}
		if e.Timing != TimingUtterance {
			return fmt.Errorf("protocol: segment timing %q is not %q", e.Timing, TimingUtterance)
		}
		d.lastStart = e.StartUS
		d.maxEndUS = max(d.maxEndUS, e.EndUS)
		d.segments++
	case *Progress:
		switch {
		case e.ConsumedUS < 0:
			return fmt.Errorf("protocol: progress consumed %d us", e.ConsumedUS)
		case e.DurationUS < 0:
			return fmt.Errorf("protocol: progress duration %d us", e.DurationUS)
		case e.ConsumedUS > e.DurationUS:
			return fmt.Errorf("protocol: progress consumed %d us of %d us", e.ConsumedUS, e.DurationUS)
		}
	case *Completed:
		switch e.Outcome {
		case OutcomeSpeech, OutcomeNoSpeech:
		default:
			return fmt.Errorf("protocol: completed outcome %q", e.Outcome)
		}
		if e.DurationUS < d.maxEndUS {
			return fmt.Errorf("protocol: completed duration %d us is shorter than the last segment end %d us", e.DurationUS, d.maxEndUS)
		}
		if e.SegmentCount != d.segments {
			return fmt.Errorf("protocol: completed claims %d segments, %d were sent", e.SegmentCount, d.segments)
		}
		if e.Outcome == OutcomeNoSpeech && d.segments != 0 {
			return fmt.Errorf("protocol: completed says %s with %d segments", OutcomeNoSpeech, e.SegmentCount)
		}
		if e.Outcome == OutcomeSpeech && d.segments == 0 {
			return fmt.Errorf("protocol: completed says %s with %d segments", OutcomeSpeech, e.SegmentCount)
		}
	case *Failed:
		if e.Code == "" {
			return errors.New("protocol: failed has no code")
		}
		if e.Message == "" {
			return errors.New("protocol: failed has no message")
		}
	}
	return nil
}

// Terminal returns the terminal event type seen so far, empty if none.
func (d *Decoder) Terminal() string { return d.terminal }

// Completed reports whether the stream ended in completed, which is half of
// the success rule; the other half is a zero exit status.
func (d *Decoder) Completed() bool { return d.terminal == TypeCompleted }
