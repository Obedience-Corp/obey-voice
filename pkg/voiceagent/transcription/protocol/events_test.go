package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEncoderMatchesSpecExamples(t *testing.T) {
	const jobID = "take-20260918-001-attempt-1"
	var buf bytes.Buffer
	enc := NewEncoder(&buf, jobID)
	for _, ev := range []Event{
		NewAccepted("parakeet", "resolved-model-id", "resolved-model-revision", TimingUtterance),
		NewSegment("s1", 800000, 2400000, "Here is the recording.", TimingUtterance),
		NewCompleted(3200000, 1, OutcomeSpeech),
	} {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("Encode(%s): %v", ev.Type(), err)
		}
	}

	want := []string{
		`{"protocol":"obey-transcript/v1","job_id":"take-20260918-001-attempt-1","seq":0,"type":"accepted","provider":"parakeet","model_id":"resolved-model-id","model_revision":"resolved-model-revision","timing":"utterance"}`,
		`{"protocol":"obey-transcript/v1","job_id":"take-20260918-001-attempt-1","seq":1,"type":"segment","segment_id":"s1","start_us":800000,"end_us":2400000,"text":"Here is the recording.","timing":"utterance","final":true}`,
		`{"protocol":"obey-transcript/v1","job_id":"take-20260918-001-attempt-1","seq":2,"type":"completed","duration_us":3200000,"segment_count":1,"outcome":"speech"}`,
	}
	got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("wrote %d lines, want %d:\n%s", len(got), len(want), buf.String())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %s\nwant %s", i, got[i], want[i])
		}
	}
}

func TestEncoderSeqAcrossAllSixTypes(t *testing.T) {
	streams := []struct {
		events []Event
		types  []string
	}{
		{
			[]Event{
				NewAccepted("parakeet", "m", "r", TimingUtterance),
				NewProgress(1_000_000, 5_000_000),
				NewSegment("s1", 0, 900_000, "one", TimingUtterance),
				NewCompleted(5_000_000, 1, OutcomeSpeech),
			},
			[]string{TypeAccepted, TypeProgress, TypeSegment, TypeCompleted},
		},
		{
			[]Event{NewFailed(CodeDecodeFailed, "decode failed", true)},
			[]string{TypeFailed},
		},
		{
			[]Event{NewCancelled()},
			[]string{TypeCancelled},
		},
	}
	for _, stream := range streams {
		var buf bytes.Buffer
		enc := NewEncoder(&buf, "job")
		for i, ev := range stream.events {
			if err := enc.Encode(ev); err != nil {
				t.Fatalf("Encode(%s): %v", stream.types[i], err)
			}
		}

		i := 0
		for line := range strings.SplitSeq(strings.TrimSuffix(buf.String(), "\n"), "\n") {
			var head struct {
				Seq  int64  `json:"seq"`
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &head); err != nil {
				t.Fatalf("line %d: %v", i, err)
			}
			if head.Seq != int64(i) {
				t.Errorf("%s seq = %d, want %d", head.Type, head.Seq, i)
			}
			if i >= len(stream.types) {
				t.Errorf("extra line %d type = %q", i, head.Type)
			} else if head.Type != stream.types[i] {
				t.Errorf("line %d type = %q, want %q", i, head.Type, stream.types[i])
			}
			i++
		}
		if i != len(stream.types) {
			t.Fatalf("wrote %d lines, want %d", i, len(stream.types))
		}
	}
}

func TestEncoderRefusesAfterEachTerminal(t *testing.T) {
	cases := []struct {
		name string
		prep []Event
		ev   Event
	}{
		{"completed", []Event{NewAccepted("parakeet", "m", "r", TimingUtterance)}, NewCompleted(1000, 0, OutcomeNoSpeech)},
		{"failed", nil, NewFailed(CodeInternal, "boom", false)},
		{"cancelled", nil, NewCancelled()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := NewEncoder(&buf, "job")
			for _, ev := range tc.prep {
				if err := enc.Encode(ev); err != nil {
					t.Fatalf("Encode(%s): %v", ev.Type(), err)
				}
			}
			if err := enc.Encode(tc.ev); err != nil {
				t.Fatalf("Encode(%s): %v", tc.ev.Type(), err)
			}
			if !enc.Terminal() {
				t.Fatal("Terminal() = false, want true")
			}
			if err := enc.Encode(NewProgress(0, 1)); !errors.Is(err, ErrAfterTerminal) {
				t.Fatalf("Encode after terminal = %v, want ErrAfterTerminal", err)
			}
			if n := strings.Count(buf.String(), "\n"); n != 1+len(tc.prep) {
				t.Fatalf("wrote %d lines, want %d:\n%s", n, 1+len(tc.prep), buf.String())
			}
		})
	}
}

func TestNewFailedFor(t *testing.T) {
	cause := errors.New("whisper crashed")
	var typedNil *Error
	cases := []struct {
		name      string
		err       error
		code      string
		message   string
		retryable bool
	}{
		{
			name:      "typed Error with cause",
			err:       &Error{Code: CodeDecodeFailed, Retryable: true, Err: cause},
			code:      CodeDecodeFailed,
			message:   cause.Error(),
			retryable: true,
		},
		{
			name:      "typed Error nil cause",
			err:       &Error{Code: CodeInputUnreadable, Retryable: false},
			code:      CodeInputUnreadable,
			message:   CodeInputUnreadable,
			retryable: false,
		},
		{
			name:      "plain error",
			err:       errors.New("boom"),
			code:      CodeInternal,
			message:   "boom",
			retryable: false,
		},
		{
			name:      "nil error",
			err:       nil,
			code:      CodeInternal,
			message:   "unspecified error",
			retryable: false,
		},
		{
			name:      "error interface holding typed-nil *Error",
			err:       typedNil,
			code:      CodeInternal,
			message:   "unspecified error",
			retryable: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *Failed
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("NewFailedFor panicked: %v", rec)
					}
				}()
				got = NewFailedFor(tc.err)
			}()
			if t.Failed() {
				return
			}
			if got == nil {
				t.Fatal("NewFailedFor returned nil")
			}
			if got.Type() != TypeFailed {
				t.Errorf("type = %q, want %q", got.Type(), TypeFailed)
			}
			if got.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Code, tc.code)
			}
			if got.Message != tc.message {
				t.Errorf("message = %q, want %q", got.Message, tc.message)
			}
			if got.Retryable != tc.retryable {
				t.Errorf("retryable = %v, want %v", got.Retryable, tc.retryable)
			}
			if coded, ok := errors.AsType[*Error](tc.err); ok && coded != nil && coded.Err != nil {
				if got.Message == coded.Error() {
					t.Errorf("message %q includes the code prefix", got.Message)
				}
			}
		})
	}
}

func TestEncoderNilEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
	}{
		{"nil interface", nil},
		{"typed-nil accepted", (*Accepted)(nil)},
		{"typed-nil progress", (*Progress)(nil)},
		{"typed-nil segment", (*Segment)(nil)},
		{"typed-nil completed", (*Completed)(nil)},
		{"typed-nil failed", (*Failed)(nil)},
		{"typed-nil cancelled", (*Cancelled)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := NewEncoder(&buf, "job")
			if err := enc.Encode(tc.ev); err == nil {
				t.Fatal("Encode returned nil, want error")
			}
			if buf.Len() != 0 {
				t.Fatalf("wrote %q, want nothing", buf.Bytes())
			}
			if enc.Terminal() {
				t.Fatal("Terminal() = true, want false")
			}
		})
	}
}

func TestEncoderFenceBeforeNilValidation(t *testing.T) {
	nils := []struct {
		name string
		ev   Event
	}{
		{"nil interface", nil},
		{"typed-nil accepted", (*Accepted)(nil)},
		{"typed-nil progress", (*Progress)(nil)},
		{"typed-nil segment", (*Segment)(nil)},
		{"typed-nil completed", (*Completed)(nil)},
		{"typed-nil failed", (*Failed)(nil)},
		{"typed-nil cancelled", (*Cancelled)(nil)},
	}

	t.Run("after terminal", func(t *testing.T) {
		for _, term := range []Event{
			NewCompleted(1000, 0, OutcomeNoSpeech),
			NewFailed(CodeInternal, "boom", false),
			NewCancelled(),
		} {
			t.Run(term.Type(), func(t *testing.T) {
				var buf bytes.Buffer
				enc := NewEncoder(&buf, "job")
				if term.Type() == TypeCompleted {
					if err := enc.Encode(NewAccepted("parakeet", "m", "r", TimingUtterance)); err != nil {
						t.Fatalf("Encode(accepted): %v", err)
					}
				}
				if err := enc.Encode(term); err != nil {
					t.Fatalf("Encode(%s): %v", term.Type(), err)
				}
				for _, tc := range nils {
					if err := enc.Encode(tc.ev); !errors.Is(err, ErrAfterTerminal) {
						t.Fatalf("Encode(%s after %s) = %v, want ErrAfterTerminal", tc.name, term.Type(), err)
					}
				}
				if !enc.Terminal() {
					t.Fatal("Terminal() = false, want true")
				}
				wantLines := 1
				if term.Type() == TypeCompleted {
					wantLines = 2
				}
				if n := strings.Count(buf.String(), "\n"); n != wantLines {
					t.Fatalf("wrote %d lines, want %d:\n%s", n, wantLines, buf.String())
				}
			})
		}
	})

	t.Run("after write fence", func(t *testing.T) {
		w := &firstWrite{n: 1}
		enc := NewEncoder(w, "job")
		if err := enc.Encode(NewAccepted("parakeet", "m", "r", TimingUtterance)); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("Encode = %v, want errors.Is ErrShortWrite", err)
		}
		for _, tc := range nils {
			if err := enc.Encode(tc.ev); !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("Encode(%s after write fence) = %v, want fenced write error", tc.name, err)
			}
		}
		if enc.Terminal() {
			t.Fatal("Terminal() = true, want false")
		}
		if w.buf.Len() != 1 {
			t.Fatalf("writer kept %d bytes, want 1", w.buf.Len())
		}
	})
}

func TestEncoderOversizedSegmentBecomesInternalFailed(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf, "job")
	err := enc.Encode(NewSegment("s1", 0, 1, strings.Repeat("a", MaxLineBytes), TimingUtterance))
	coded, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("Encode oversized = %v, want *Error", err)
	}
	if coded.Code != CodeInternal {
		t.Fatalf("code = %q, want %q", coded.Code, CodeInternal)
	}
	if !enc.Terminal() {
		t.Fatal("Terminal() = false, want true")
	}

	line, ok := strings.CutSuffix(buf.String(), "\n")
	if !ok || strings.Contains(line, "\n") {
		t.Fatalf("output = %q, want exactly one newline-terminated line", buf.String())
	}
	if buf.Len() > MaxLineBytes {
		t.Fatalf("line is %d bytes including newline, over the %d cap", buf.Len(), MaxLineBytes)
	}
	var failed Failed
	if err := json.Unmarshal([]byte(line), &failed); err != nil {
		t.Fatalf("substitute line: %v", err)
	}
	if failed.Type() != TypeFailed || failed.Code != CodeInternal || failed.SeqOf() != 0 {
		t.Fatalf("substitute = type %q code %q seq %d, want failed %s seq 0", failed.Type(), failed.Code, failed.SeqOf(), CodeInternal)
	}
	if err := enc.Encode(NewCancelled()); !errors.Is(err, ErrAfterTerminal) {
		t.Fatalf("Encode after overflow = %v, want ErrAfterTerminal", err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("wrote %d lines after later Encode, want 1:\n%s", n, buf.String())
	}
}

func TestEncoderOverflowSubstituteCannotFit(t *testing.T) {
	jobID := strings.Repeat("j", MaxLineBytes)
	var buf bytes.Buffer
	enc := NewEncoder(&buf, jobID)
	err := enc.Encode(NewCancelled())
	coded, ok := errors.AsType[*Error](err)
	if !ok || coded.Code != CodeInternal {
		t.Fatalf("Encode oversized = %v, want *Error code %s", err, CodeInternal)
	}
	if enc.Terminal() {
		t.Fatal("Terminal() = true, want false (no terminal was written)")
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %q, want nothing", buf.Bytes())
	}

	later := enc.Encode(NewFailed(CodeInternal, "boom", false))
	if !errors.Is(later, err) {
		t.Fatalf("later Encode = %v, want the overflow error %v", later, err)
	}
	if enc.Terminal() {
		t.Fatal("Terminal() = true after fenced later Encode")
	}
	if buf.Len() != 0 {
		t.Fatalf("later Encode wrote %q, want nothing", buf.Bytes())
	}
}

func TestEncoderLineCapExactBoundary(t *testing.T) {
	const jobID = "job"
	emptyLine := marshalEventLine(t, NewSegment("s1", 0, 1, "", TimingUtterance), jobID, 1)
	textLen := MaxLineBytes - 1 - len(emptyLine)
	if textLen < 1 {
		t.Fatalf("empty segment JSON is %d bytes; cannot grow to the %d byte cap", len(emptyLine)+1, MaxLineBytes)
	}
	exactText := strings.Repeat("a", textLen)
	exactLine := marshalEventLine(t, NewSegment("s1", 0, 1, exactText, TimingUtterance), jobID, 1)
	if got := len(exactLine) + 1; got != MaxLineBytes {
		t.Fatalf("derived exact line is %d bytes including newline, want %d", got, MaxLineBytes)
	}

	var buf bytes.Buffer
	enc := NewEncoder(&buf, jobID)
	if err := enc.Encode(NewAccepted("parakeet", "m", "r", TimingUtterance)); err != nil {
		t.Fatalf("Encode(accepted): %v", err)
	}
	before := buf.Len()
	if err := enc.Encode(NewSegment("s1", 0, 1, exactText, TimingUtterance)); err != nil {
		t.Fatalf("Encode exact cap: %v", err)
	}
	if buf.Len()-before != MaxLineBytes {
		t.Fatalf("segment line is %d bytes, want %d", buf.Len()-before, MaxLineBytes)
	}
	if enc.Terminal() {
		t.Fatal("Terminal() = true after exact-cap segment")
	}
	gotLine, ok := strings.CutSuffix(buf.String()[before:], "\n")
	if !ok {
		t.Fatal("exact-cap output is not newline-terminated")
	}
	var seg Segment
	if err := json.Unmarshal([]byte(gotLine), &seg); err != nil {
		t.Fatalf("exact-cap line: %v", err)
	}
	if seg.Type() != TypeSegment {
		t.Fatalf("type = %q, want %q", seg.Type(), TypeSegment)
	}

	overText := strings.Repeat("a", textLen+1)
	overLine := marshalEventLine(t, NewSegment("s1", 0, 1, overText, TimingUtterance), jobID, 0)
	if len(overLine)+1 <= MaxLineBytes {
		t.Fatalf("one extra byte still fits: %d including newline", len(overLine)+1)
	}

	var overBuf bytes.Buffer
	overEnc := NewEncoder(&overBuf, jobID)
	err := overEnc.Encode(NewSegment("s1", 0, 1, overText, TimingUtterance))
	coded, ok := errors.AsType[*Error](err)
	if !ok || coded.Code != CodeInternal {
		t.Fatalf("Encode one-past cap = %v, want *Error code %s", err, CodeInternal)
	}
	if !overEnc.Terminal() {
		t.Fatal("Terminal() = false after overflow substitution")
	}
	failedLine, ok := strings.CutSuffix(overBuf.String(), "\n")
	if !ok || strings.Contains(failedLine, "\n") {
		t.Fatalf("overflow output = %q, want one newline-terminated line", overBuf.String())
	}
	if overBuf.Len() > MaxLineBytes {
		t.Fatalf("substitute line is %d bytes including newline, over the %d cap", overBuf.Len(), MaxLineBytes)
	}
	var failed Failed
	if err := json.Unmarshal([]byte(failedLine), &failed); err != nil {
		t.Fatalf("substitute line: %v", err)
	}
	if failed.Type() != TypeFailed || failed.Code != CodeInternal || failed.SeqOf() != 0 {
		t.Fatalf("substitute = type %q code %q seq %d, want failed %s seq 0", failed.Type(), failed.Code, failed.SeqOf(), CodeInternal)
	}
}

func TestEncoderWriterFailures(t *testing.T) {
	accepted := func() Event {
		return NewAccepted("parakeet", "m", "r", TimingUtterance)
	}

	t.Run("error before bytes", func(t *testing.T) {
		underlying := errors.New("disk full")
		w := &firstWrite{n: 0, err: underlying}
		enc := NewEncoder(w, "job")
		if err := enc.Encode(accepted()); !errors.Is(err, underlying) {
			t.Fatalf("Encode = %v, want errors.Is underlying", err)
		}
		if w.buf.Len() != 0 {
			t.Fatalf("wrote %q before the error, want nothing", w.buf.Bytes())
		}
		if err := enc.Encode(accepted()); err != nil {
			t.Fatalf("retry Encode: %v", err)
		}
		requireOneSeq0Line(t, w.buf.String())
	})

	t.Run("short count nil", func(t *testing.T) {
		w := &firstWrite{n: 0}
		enc := NewEncoder(w, "job")
		if err := enc.Encode(accepted()); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("Encode = %v, want errors.Is ErrShortWrite", err)
		}
		if w.buf.Len() != 0 {
			t.Fatalf("empty short write kept %q, want nothing", w.buf.Bytes())
		}
		if err := enc.Encode(accepted()); err != nil {
			t.Fatalf("retry Encode: %v", err)
		}
		requireOneSeq0Line(t, w.buf.String())
	})

	t.Run("partial short write", func(t *testing.T) {
		const prefix = 1
		w := &firstWrite{n: prefix}
		enc := NewEncoder(w, "job")
		if err := enc.Encode(accepted()); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("Encode = %v, want errors.Is ErrShortWrite", err)
		}
		if w.buf.Len() != prefix {
			t.Fatalf("writer kept %d bytes, want %d", w.buf.Len(), prefix)
		}
		err := enc.Encode(NewProgress(1, 2))
		if err != nil {
			return
		}
		t.Fatalf("Encode after a %d-byte short write succeeded; seq was not fenced and JSONL is corrupt:\n%q", prefix, w.buf.String())
	})
}

func TestEncoderInvalidUTF8SegmentText(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf, "job")
	if err := enc.Encode(NewAccepted("parakeet", "m", "r", TimingUtterance)); err != nil {
		t.Fatalf("Encode(accepted): %v", err)
	}
	if err := enc.Encode(NewSegment("s1", 0, 1, "pre\xffpost", TimingUtterance)); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !utf8.Valid(buf.Bytes()) {
		t.Fatalf("JSONL is not valid UTF-8: %q", buf.Bytes())
	}
	raw := strings.TrimSuffix(buf.String(), "\n")
	lines := strings.Split(raw, "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want 2:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("JSONL line %d is not valid JSON: %q", i, line)
		}
	}
}

func marshalEventLine(t *testing.T, ev Event, jobID string, seq int64) []byte {
	t.Helper()
	ev.stamp(Protocol, jobID, seq)
	line, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return line
}

func requireOneSeq0Line(t *testing.T, raw string) {
	t.Helper()
	line, ok := strings.CutSuffix(raw, "\n")
	if !ok || strings.Contains(line, "\n") {
		t.Fatalf("retry wrote %q, want one newline-terminated line", raw)
	}
	var head struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal([]byte(line), &head); err != nil {
		t.Fatalf("retry line: %v", err)
	}
	if head.Seq != 0 {
		t.Fatalf("seq = %d, want 0 (write failure must not advance seq)", head.Seq)
	}
}

type firstWrite struct {
	n    int
	err  error
	buf  bytes.Buffer
	used bool
}

func (w *firstWrite) Write(p []byte) (int, error) {
	if !w.used {
		w.used = true
		n := min(w.n, len(p))
		if n > 0 {
			w.buf.Write(p[:n])
		}
		return n, w.err
	}
	return w.buf.Write(p)
}

func TestDecoderRoundTrip(t *testing.T) {
	const jobID = "take-20260918-001-attempt-1"
	for _, term := range []Event{
		NewCompleted(3_200_000, 1, OutcomeSpeech),
		NewFailed(CodeDecodeFailed, "decode failed", true),
		NewCancelled(),
	} {
		t.Run(term.Type(), func(t *testing.T) {
			evs := []Event{
				NewAccepted("parakeet", "resolved-model-id", "resolved-model-revision", TimingUtterance),
				NewProgress(1_000_000, 5_000_000),
				NewSegment("s1", 800_000, 2_400_000, "Here is the recording.", TimingUtterance),
				term,
			}
			dec := NewDecoder(strings.NewReader(encodeJSONL(t, jobID, evs...)), jobID)
			for i, want := range evs {
				got, err := dec.Next()
				if err != nil {
					t.Fatalf("event %d: %v", i, err)
				}
				if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", want) {
					t.Fatalf("event %d: got %T, want %T", i, got, want)
				}
				if got.Type() != want.Type() || got.JobIDOf() != jobID || got.SeqOf() != int64(i) {
					t.Fatalf("event %d: type=%q job=%q seq=%d, want %q %q %d", i, got.Type(), got.JobIDOf(), got.SeqOf(), want.Type(), jobID, i)
				}
				switch g := got.(type) {
				case *Accepted:
					if g.Provider != "parakeet" || g.ModelID != "resolved-model-id" || g.ModelRevision != "resolved-model-revision" || g.Timing != TimingUtterance {
						t.Fatalf("accepted = %+v", g)
					}
				case *Progress:
					if g.ConsumedUS != 1_000_000 || g.DurationUS != 5_000_000 {
						t.Fatalf("progress = %+v", g)
					}
				case *Segment:
					if g.SegmentID != "s1" || g.StartUS != 800_000 || g.EndUS != 2_400_000 || g.Text != "Here is the recording." || g.Timing != TimingUtterance || !g.Final {
						t.Fatalf("segment = %+v", g)
					}
				case *Completed:
					if g.DurationUS != 3_200_000 || g.SegmentCount != 1 || g.Outcome != OutcomeSpeech {
						t.Fatalf("completed = %+v", g)
					}
				case *Failed:
					if g.Code != CodeDecodeFailed || g.Message != "decode failed" || !g.Retryable {
						t.Fatalf("failed = %+v", g)
					}
				}
				wantTerm := ""
				if i == len(evs)-1 {
					wantTerm = want.Type()
				}
				if dec.Terminal() != wantTerm {
					t.Fatalf("event %d Terminal() = %q, want %q", i, dec.Terminal(), wantTerm)
				}
				if dec.Completed() != (wantTerm == TypeCompleted) {
					t.Fatalf("event %d Completed() = %v, want %v", i, dec.Completed(), wantTerm == TypeCompleted)
				}
			}
			if _, err := dec.Next(); !errors.Is(err, io.EOF) {
				t.Fatalf("after terminal: %v, want io.EOF", err)
			}
		})
	}
}

func TestDecoderJobID(t *testing.T) {
	line := func(ev Event, jobID string, seq int64) string {
		t.Helper()
		return string(marshalEventLine(t, ev, jobID, seq))
	}
	accepted := NewAccepted("parakeet", "m", "r", TimingUtterance)
	cases := []struct {
		name, expected, wantJob, wantErr string
		lines                            []string
		wantN                            int
	}{
		{
			name:     "adopts nonempty first",
			expected: "",
			lines:    []string{line(accepted, "job-a", 0), line(NewCancelled(), "job-a", 1)},
			wantN:    2,
			wantJob:  "job-a",
		},
		{
			name:     "empty first job_id",
			expected: "",
			lines:    []string{line(NewCancelled(), "", 0)},
			wantErr:  "event has no job_id",
		},
		{
			name:     "expected mismatch",
			expected: "want",
			lines:    []string{line(NewCancelled(), "other", 0)},
			wantErr:  `event job_id "other" is not "want"`,
		},
		{
			name:     "later change after adopt",
			expected: "",
			lines:    []string{line(accepted, "job-a", 0), line(NewProgress(1, 2), "job-b", 1)},
			wantN:    1,
			wantJob:  "job-a",
			wantErr:  `event job_id "job-b" is not "job-a"`,
		},
		{
			name:     "later change after expected",
			expected: "job-a",
			lines:    []string{line(accepted, "job-a", 0), line(NewProgress(1, 2), "job-b", 1)},
			wantN:    1,
			wantJob:  "job-a",
			wantErr:  `event job_id "job-b" is not "job-a"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := decodeStream(t, tc.expected, strings.Join(tc.lines, "\n")+"\n")
			if len(got) != tc.wantN {
				t.Fatalf("decoded %d events, want %d (err = %v)", len(got), tc.wantN, err)
			}
			for i, ev := range got {
				if ev.JobIDOf() != tc.wantJob {
					t.Fatalf("event %d JobIDOf() = %q, want %q", i, ev.JobIDOf(), tc.wantJob)
				}
			}
			if tc.wantErr == "" {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("err = %v, want io.EOF", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestDecoderEnvelope(t *testing.T) {
	line := func(ev Event, seq int64) string {
		t.Helper()
		return string(marshalEventLine(t, ev, "job", seq))
	}
	accepted := line(NewAccepted("parakeet", "m", "r", TimingUtterance), 0)
	cases := []struct {
		name    string
		lines   []string
		wantN   int
		wantErr string
	}{
		{"wrong protocol", []string{`{"protocol":"obey-transcript/v0","job_id":"job","seq":0,"type":"cancelled"}`}, 0, `event protocol "obey-transcript/v0" is not "` + Protocol + `"`},
		{"missing protocol", []string{`{"job_id":"job","seq":0,"type":"cancelled"}`}, 0, `event protocol "" is not "` + Protocol + `"`},
		{"missing seq", []string{`{"protocol":"` + Protocol + `","job_id":"job","type":"cancelled"}`}, 0, "event has no seq"},
		{"seq 0", []string{line(NewCancelled(), 0)}, 1, ""},
		{"start not zero", []string{line(NewCancelled(), 1)}, 0, "event seq 1 is not 0"},
		{"seq gap", []string{accepted, line(NewCancelled(), 2)}, 1, "event seq 2 is not 1"},
		{"seq duplicate", []string{accepted, line(NewCancelled(), 0)}, 1, "event seq 0 is not 1"},
		{"missing type", []string{`{"protocol":"` + Protocol + `","job_id":"job","seq":0}`}, 0, `unknown event type ""`},
		{"empty type", []string{`{"protocol":"` + Protocol + `","job_id":"job","seq":0,"type":""}`}, 0, `unknown event type ""`},
		{"unknown type", []string{`{"protocol":"` + Protocol + `","job_id":"job","seq":0,"type":"partial"}`}, 0, `unknown event type "partial"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := decodeStream(t, "job", strings.Join(tc.lines, "\n")+"\n")
			if len(got) != tc.wantN {
				t.Fatalf("decoded %d events, want %d (err = %v)", len(got), tc.wantN, err)
			}
			if tc.wantErr == "" {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("err = %v, want io.EOF", err)
				}
				if got[0].Type() != TypeCancelled || got[0].SeqOf() != 0 {
					t.Fatalf("got type %q seq %d, want %s seq 0", got[0].Type(), got[0].SeqOf(), TypeCancelled)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestDecoderBlankLineAndMalformedJSON(t *testing.T) {
	accepted := encodeJSONL(t, "job", NewAccepted("parakeet", "m", "r", TimingUtterance))
	cases := []struct {
		name    string
		raw     string
		wantN   int
		wantErr string
		syntax  bool
	}{
		{"leading blank", "\n", 0, "blank line in the event stream", false},
		{"blank between events", accepted + "\n" + encodeJSONL(t, "job", NewCancelled()), 1, "blank line in the event stream", false},
		{"malformed", "{", 0, "parse event:", true},
		{"truncated", `{"protocol":"` + Protocol, 0, "parse event:", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := decodeStream(t, "job", tc.raw)
			if len(got) != tc.wantN {
				t.Fatalf("decoded %d events, want %d (err = %v)", len(got), tc.wantN, err)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if tc.syntax {
				if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
					t.Fatalf("errors.AsType cannot reach json.SyntaxError: %v", err)
				}
			}
		})
	}
}

func TestDecoderTerminalLifecycle(t *testing.T) {
	cancelled := encodeJSONL(t, "job", NewCancelled())
	cases := []struct {
		name      string
		raw       string
		wantN     int
		wantErr   error
		terminal  string
		completed bool
	}{
		{"empty stream", "", 0, ErrNoTerminal, "", false},
		{"accepted only", encodeJSONL(t, "job", NewAccepted("parakeet", "m", "r", TimingUtterance)), 1, ErrNoTerminal, "", false},
		{"after completed", encodeJSONL(t, "job", NewAccepted("parakeet", "m", "r", TimingUtterance), NewCompleted(0, 0, OutcomeNoSpeech)), 2, io.EOF, TypeCompleted, true},
		{"after failed", encodeJSONL(t, "job", NewFailed(CodeInternal, "boom", false)), 1, io.EOF, TypeFailed, false},
		{"after cancelled", cancelled, 1, io.EOF, TypeCancelled, false},
		{"event after terminal", cancelled + encodeJSONL(t, "job", NewCancelled()), 1, ErrAfterTerminal, TypeCancelled, false},
		{"garbage after terminal", cancelled + "junk\n", 1, ErrAfterTerminal, TypeCancelled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, dec, err := decodeStream(t, "job", tc.raw)
			if len(got) != tc.wantN {
				t.Fatalf("decoded %d events, want %d (err = %v)", len(got), tc.wantN, err)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
			}
			if dec.Terminal() != tc.terminal {
				t.Fatalf("Terminal() = %q, want %q", dec.Terminal(), tc.terminal)
			}
			if dec.Completed() != tc.completed {
				t.Fatalf("Completed() = %v, want %v", dec.Completed(), tc.completed)
			}
		})
	}
}

func TestDecoderLineCap(t *testing.T) {
	exact := paddedJSON(t, MaxLineBytes-1)
	over := paddedJSON(t, MaxLineBytes)
	cases := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"exact with newline", exact + "\n", nil},
		{"exact without newline", exact, nil},
		{"one over with newline", over + "\n", ErrLineTooLong},
		{"one over without newline", over, ErrLineTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := NewDecoder(strings.NewReader(tc.raw), "job").Next()
			if tc.wantErr != nil {
				if ev != nil {
					t.Fatalf("got %T, want no event", ev)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			got, ok := ev.(*Cancelled)
			if !ok {
				t.Fatalf("got %T, want *Cancelled", ev)
			}
			if got.Type() != TypeCancelled || got.JobIDOf() != "job" || got.SeqOf() != 0 {
				t.Fatalf("type=%q job=%q seq=%d", got.Type(), got.JobIDOf(), got.SeqOf())
			}
		})
	}
}

func TestDecoderReadError(t *testing.T) {
	underlying := errors.New("pipe broken")
	accepted := encodeJSONL(t, "job", NewAccepted("parakeet", "m", "r", TimingUtterance))
	cases := []struct {
		name string
		rest []byte
		next bool
	}{
		{"before bytes", nil, false},
		{"mid-line", []byte(`{"protocol":"`), false},
		{"after non-terminal", []byte(accepted), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := NewDecoder(&errAfter{rest: append([]byte(nil), tc.rest...), err: underlying}, "job")
			if tc.next {
				ev, err := dec.Next()
				if err != nil {
					t.Fatalf("first Next: %v", err)
				}
				if ev.Type() != TypeAccepted {
					t.Fatalf("first event type = %q, want %s", ev.Type(), TypeAccepted)
				}
			}
			_, err := dec.Next()
			if !errors.Is(err, underlying) {
				t.Fatalf("Next = %v, want errors.Is underlying", err)
			}
			if errors.Is(err, ErrNoTerminal) || errors.Is(err, io.EOF) {
				t.Fatalf("Next = %v, I/O error must not become a stream-end sentinel", err)
			}
			if !strings.Contains(err.Error(), "read event stream") {
				t.Fatalf("Next = %v, want wrapped read error", err)
			}
		})
	}
}

func encodeJSONL(t *testing.T, jobID string, evs ...Event) string {
	t.Helper()
	var buf bytes.Buffer
	enc := NewEncoder(&buf, jobID)
	for _, ev := range evs {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("Encode(%s): %v", ev.Type(), err)
		}
	}
	return buf.String()
}

func decodeStream(t *testing.T, jobID, raw string) ([]Event, *Decoder, error) {
	t.Helper()
	dec := NewDecoder(strings.NewReader(raw), jobID)
	var got []Event
	for {
		ev, err := dec.Next()
		if err != nil {
			return got, dec, err
		}
		got = append(got, ev)
	}
}

func paddedJSON(t *testing.T, n int) string {
	t.Helper()
	prefix := `{"protocol":"` + Protocol + `","job_id":"job","seq":0,"type":"cancelled","pad":"`
	suffix := `"}`
	pad := n - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("target %d is smaller than overhead %d", n, len(prefix)+len(suffix))
	}
	s := prefix + strings.Repeat("x", pad) + suffix
	if len(s) != n {
		t.Fatalf("padded len = %d, want %d", len(s), n)
	}
	if !json.Valid([]byte(s)) {
		t.Fatal("padded JSON is invalid")
	}
	return s
}

type errAfter struct {
	rest []byte
	err  error
}

func (r *errAfter) Read(p []byte) (int, error) {
	if len(r.rest) == 0 {
		return 0, r.err
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

func TestDecoderSegmentSemantics(t *testing.T) {
	accepted := NewAccepted("parakeet", "m", "r", TimingUtterance)
	notFinal := NewSegment("s1", 0, 1, "x", TimingUtterance)
	notFinal.Final = false
	cases := []struct {
		name    string
		events  []Event
		wantN   int
		wantErr string
	}{
		{
			name:    "negative start",
			events:  []Event{accepted, NewSegment("s1", -1, 1000, "x", TimingUtterance)},
			wantN:   1,
			wantErr: "starts at -1 us",
		},
		{
			name:    "zero interval",
			events:  []Event{accepted, NewSegment("s1", 900, 900, "x", TimingUtterance)},
			wantN:   1,
			wantErr: "spans 900..900",
		},
		{
			name:    "inverted interval",
			events:  []Event{accepted, NewSegment("s1", 900, 100, "x", TimingUtterance)},
			wantN:   1,
			wantErr: "spans 900..100",
		},
		{
			name: "decreasing starts",
			events: []Event{
				accepted,
				NewSegment("s1", 2000, 3000, "x", TimingUtterance),
				NewSegment("s2", 1000, 1500, "y", TimingUtterance),
			},
			wantN:   2,
			wantErr: "before the previous segment",
		},
		{
			name: "equal starts allowed",
			events: []Event{
				accepted,
				NewSegment("s1", 1000, 1500, "x", TimingUtterance),
				NewSegment("s2", 1000, 2000, "y", TimingUtterance),
				NewCompleted(2000, 2, OutcomeSpeech),
			},
			wantN: 4,
		},
		{
			name:    "empty segment_id",
			events:  []Event{accepted, NewSegment("", 0, 1, "x", TimingUtterance)},
			wantN:   1,
			wantErr: "segment has no segment_id",
		},
		{
			name:    "final false",
			events:  []Event{accepted, notFinal},
			wantN:   1,
			wantErr: "not final",
		},
		{
			name:    "empty timing",
			events:  []Event{accepted, NewSegment("s1", 0, 1, "x", "")},
			wantN:   1,
			wantErr: `segment timing ""`,
		},
		{
			name:    "timing none",
			events:  []Event{accepted, NewSegment("s1", 0, 1, "x", TimingNone)},
			wantN:   1,
			wantErr: `segment timing "none"`,
		},
		{
			name:    "unknown timing",
			events:  []Event{accepted, NewSegment("s1", 0, 1, "x", "partial")},
			wantN:   1,
			wantErr: `segment timing "partial"`,
		},
		{
			name: "utterance timing",
			events: []Event{
				accepted,
				NewSegment("s1", 0, 1, "x", TimingUtterance),
				NewCompleted(1, 1, OutcomeSpeech),
			},
			wantN: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := decodeStream(t, "job", encodeJSONL(t, "job", tc.events...))
			requireDecoded(t, got, err, tc.wantN, tc.wantErr)
		})
	}
}

func TestDecoderCompletedSemantics(t *testing.T) {
	accepted := NewAccepted("parakeet", "m", "r", TimingUtterance)
	seg := NewSegment("s1", 0, 3000, "x", TimingUtterance)
	cases := []struct {
		name    string
		events  []Event
		wantN   int
		wantErr string
	}{
		{
			name:    "unknown outcome",
			events:  []Event{accepted, NewCompleted(10, 0, "partial")},
			wantN:   1,
			wantErr: `completed outcome "partial"`,
		},
		{
			name:    "negative duration",
			events:  []Event{accepted, NewCompleted(-1, 0, OutcomeNoSpeech)},
			wantN:   1,
			wantErr: "duration -1 us",
		},
		{
			name:    "shorter than max segment end",
			events:  []Event{accepted, seg, NewCompleted(2000, 1, OutcomeSpeech)},
			wantN:   2,
			wantErr: "shorter than the last segment end",
		},
		{
			name:    "declared count exceeds actual",
			events:  []Event{accepted, NewCompleted(10, 1, OutcomeSpeech)},
			wantN:   1,
			wantErr: "claims 1 segments",
		},
		{
			name:    "declared count under actual",
			events:  []Event{accepted, seg, NewCompleted(3000, 0, OutcomeNoSpeech)},
			wantN:   2,
			wantErr: "claims 0 segments",
		},
		{
			name:    "no_speech with actual segments",
			events:  []Event{accepted, seg, NewCompleted(3000, 1, OutcomeNoSpeech)},
			wantN:   2,
			wantErr: "no_speech with 1 segments",
		},
		{
			name:    "speech with zero segments",
			events:  []Event{accepted, NewCompleted(10, 0, OutcomeSpeech)},
			wantN:   1,
			wantErr: "speech with 0 segments",
		},
		{
			name:   "no_speech with zero segments",
			events: []Event{accepted, NewCompleted(10, 0, OutcomeNoSpeech)},
			wantN:  2,
		},
		{
			name:   "speech with matching segments",
			events: []Event{accepted, seg, NewCompleted(3000, 1, OutcomeSpeech)},
			wantN:  3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := decodeStream(t, "job", encodeJSONL(t, "job", tc.events...))
			requireDecoded(t, got, err, tc.wantN, tc.wantErr)
		})
	}
}

func TestDecoderProgressSemantics(t *testing.T) {
	accepted := NewAccepted("parakeet", "m", "r", TimingUtterance)
	cases := []struct {
		name    string
		ev      Event
		wantErr string
	}{
		{"negative consumed", NewProgress(-1, 10), "progress consumed -1 us"},
		{"negative duration", NewProgress(0, -1), "progress duration -1 us"},
		{"consumed after duration", NewProgress(11, 10), "progress consumed 11 us of 10 us"},
		{"zero of zero", NewProgress(0, 0), ""},
		{"zero of duration", NewProgress(0, 10), ""},
		{"mid duration", NewProgress(5, 10), ""},
		{"consumed equals duration", NewProgress(10, 10), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := []Event{accepted, tc.ev}
			wantN := 1
			if tc.wantErr == "" {
				events = append(events, NewCancelled())
				wantN = 3
			}
			got, _, err := decodeStream(t, "job", encodeJSONL(t, "job", events...))
			requireDecoded(t, got, err, wantN, tc.wantErr)
		})
	}
}

func TestDecoderStateRollback(t *testing.T) {
	accepted := NewAccepted("parakeet", "m", "r", TimingUtterance)
	good := NewSegment("s1", 1000, 2000, "one", TimingUtterance)
	notFinal := NewSegment("s2", 3000, 4000, "two", TimingUtterance)
	notFinal.Final = false
	correction := NewSegment("s2", 1000, 2500, "two", TimingUtterance)
	done := NewCompleted(2500, 2, OutcomeSpeech)

	dec := NewDecoder(strings.NewReader(strings.Join([]string{
		string(marshalEventLine(t, accepted, "job", 0)),
		string(marshalEventLine(t, good, "job", 1)),
		string(marshalEventLine(t, notFinal, "job", 2)),
		string(marshalEventLine(t, correction, "job", 2)),
		string(marshalEventLine(t, done, "job", 3)),
	}, "\n")+"\n"), "job")

	if ev, err := dec.Next(); err != nil || ev.Type() != TypeAccepted {
		t.Fatalf("seq 0: %T %v", ev, err)
	}
	if ev, err := dec.Next(); err != nil || ev.Type() != TypeSegment {
		t.Fatalf("seq 1: %T %v", ev, err)
	}
	if ev, err := dec.Next(); err == nil || !strings.Contains(err.Error(), "not final") {
		t.Fatalf("invalid seq 2: %T %v, want not final", ev, err)
	}
	got, err := dec.Next()
	if err != nil {
		t.Fatalf("corrected seq 2: %v (invalid segment mutated lastStart/maxEnd/count)", err)
	}
	seg, ok := got.(*Segment)
	if !ok || seg.SegmentID != "s2" || seg.StartUS != 1000 || seg.SeqOf() != 2 {
		t.Fatalf("corrected seq 2 = %+v", got)
	}
	ev, err := dec.Next()
	if err != nil {
		t.Fatalf("completed: %v (invalid segment counted toward SegmentCount)", err)
	}
	if c, ok := ev.(*Completed); !ok || c.SegmentCount != 2 || c.SeqOf() != 3 {
		t.Fatalf("completed = %+v", ev)
	}
	if _, err := dec.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after terminal: %v, want io.EOF", err)
	}
}

func TestDecoderFailedRequiresCodeAndMessage(t *testing.T) {
	cases := []struct {
		name    string
		ev      Event
		wantErr string
	}{
		{"empty code", NewFailed("", "boom", false), "failed has no code"},
		{"empty message", NewFailed(CodeInternal, "", false), "failed has no message"},
		{"stable code and message", NewFailed(CodeDecodeFailed, "decode failed", true), ""},
		{"unknown code is not KnownCode", NewFailed("not_a_catalog_code", "backend exploded", false), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := decodeStream(t, "job", encodeJSONL(t, "job", tc.ev))
			wantN := 0
			if tc.wantErr == "" {
				wantN = 1
			}
			requireDecoded(t, got, err, wantN, tc.wantErr)
		})
	}
}

func TestDecoderCancelledHasNoExtraSemantics(t *testing.T) {
	got, dec, err := decodeStream(t, "job", encodeJSONL(t, "job", NewCancelled()))
	requireDecoded(t, got, err, 1, "")
	if got[0].Type() != TypeCancelled || got[0].SeqOf() != 0 {
		t.Fatalf("got type %q seq %d, want %s seq 0", got[0].Type(), got[0].SeqOf(), TypeCancelled)
	}
	if dec.Terminal() != TypeCancelled || dec.Completed() {
		t.Fatalf("Terminal() = %q Completed() = %v", dec.Terminal(), dec.Completed())
	}
}

func requireDecoded(t *testing.T, got []Event, err error, wantN int, wantErr string) {
	t.Helper()
	if len(got) != wantN {
		t.Fatalf("decoded %d events, want %d (err = %v)", len(got), wantN, err)
	}
	if wantErr == "" {
		if !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v, want io.EOF", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("err = %v, want containing %q", err, wantErr)
	}
}
