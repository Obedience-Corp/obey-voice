package protocol

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestEncoderAcceptedOrder(t *testing.T) {
	accepted := func() Event {
		return NewAccepted("parakeet", "m", "r", TimingUtterance)
	}

	t.Run("refuses progress segment and completed before accepted", func(t *testing.T) {
		for _, ev := range []Event{
			NewProgress(1, 2),
			NewSegment("s1", 0, 1, "x", TimingUtterance),
			NewCompleted(1, 0, OutcomeNoSpeech),
		} {
			var buf bytes.Buffer
			enc := NewEncoder(&buf, "job")
			err := enc.Encode(ev)
			if !errors.Is(err, ErrBeforeAccepted) {
				t.Fatalf("Encode(%s) = %v, want ErrBeforeAccepted", ev.Type(), err)
			}
			if buf.Len() != 0 {
				t.Fatalf("Encode(%s) wrote %q", ev.Type(), buf.Bytes())
			}
			if enc.Terminal() {
				t.Fatalf("Encode(%s) set Terminal", ev.Type())
			}
		}
	})

	t.Run("refuses a second accepted", func(t *testing.T) {
		var buf bytes.Buffer
		enc := NewEncoder(&buf, "job")
		if err := enc.Encode(accepted()); err != nil {
			t.Fatalf("first accepted: %v", err)
		}
		if err := enc.Encode(accepted()); !errors.Is(err, ErrDuplicateAccepted) {
			t.Fatalf("second accepted = %v, want ErrDuplicateAccepted", err)
		}
		if n := strings.Count(buf.String(), "\n"); n != 1 {
			t.Fatalf("wrote %d lines, want 1:\n%s", n, buf.String())
		}
	})

	t.Run("failed and cancelled may open the stream", func(t *testing.T) {
		for _, ev := range []Event{
			NewFailed(CodeInternal, "preflight", false),
			NewCancelled(),
		} {
			var buf bytes.Buffer
			enc := NewEncoder(&buf, "job")
			if err := enc.Encode(ev); err != nil {
				t.Fatalf("Encode(%s): %v", ev.Type(), err)
			}
			if !enc.Terminal() {
				t.Fatalf("Terminal() = false after %s", ev.Type())
			}
			if n := strings.Count(buf.String(), "\n"); n != 1 {
				t.Fatalf("%s wrote %d lines, want 1", ev.Type(), n)
			}
		}
	})

	t.Run("refused completed does not consume seq", func(t *testing.T) {
		var buf bytes.Buffer
		enc := NewEncoder(&buf, "job")
		if err := enc.Encode(NewCompleted(1, 0, OutcomeNoSpeech)); !errors.Is(err, ErrBeforeAccepted) {
			t.Fatalf("Encode(completed) = %v, want ErrBeforeAccepted", err)
		}
		if err := enc.Encode(NewFailed(CodeInternal, "preflight", false)); err != nil {
			t.Fatalf("Encode(failed) after refused completed: %v", err)
		}
		requireOneSeq0Line(t, buf.String())
	})

	t.Run("accepted is recorded only after a successful write", func(t *testing.T) {
		underlying := errors.New("disk full")
		w := &firstWrite{n: 0, err: underlying}
		enc := NewEncoder(w, "job")
		if err := enc.Encode(accepted()); !errors.Is(err, underlying) {
			t.Fatalf("Encode(accepted) = %v, want underlying write error", err)
		}
		if err := enc.Encode(NewProgress(1, 2)); !errors.Is(err, ErrBeforeAccepted) {
			t.Fatalf("Encode(progress) after failed accepted write = %v, want ErrBeforeAccepted", err)
		}
		if err := enc.Encode(accepted()); err != nil {
			t.Fatalf("retry Encode(accepted): %v", err)
		}
		if err := enc.Encode(NewProgress(1, 2)); err != nil {
			t.Fatalf("Encode(progress) after written accepted: %v", err)
		}
		if n := strings.Count(w.buf.String(), "\n"); n != 2 {
			t.Fatalf("wrote %d lines, want 2:\n%s", n, w.buf.String())
		}
	})

	t.Run("partial accepted write does not record accepted", func(t *testing.T) {
		w := &firstWrite{n: 1}
		enc := NewEncoder(w, "job")
		if err := enc.Encode(accepted()); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("Encode(accepted) = %v, want ErrShortWrite", err)
		}
		err := enc.Encode(NewProgress(1, 2))
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("Encode after partial accepted write = %v, want fenced write error", err)
		}
		if enc.Terminal() {
			t.Fatal("Terminal() = true after fenced accepted write")
		}
		if w.buf.Len() != 1 {
			t.Fatalf("writer kept %d bytes, want 1", w.buf.Len())
		}
	})
}

func TestDecoderAcceptedOrder(t *testing.T) {
	line := func(ev Event, seq int64) string {
		t.Helper()
		return string(marshalEventLine(t, ev, "job", seq))
	}
	accepted := line(NewAccepted("parakeet", "m", "r", TimingUtterance), 0)

	t.Run("refuses progress segment and completed before accepted", func(t *testing.T) {
		cases := []struct {
			name string
			raw  string
			kind string
		}{
			{"progress", line(NewProgress(1, 2), 0) + "\n", TypeProgress},
			{"segment", line(NewSegment("s1", 0, 1, "x", TimingUtterance), 0) + "\n", TypeSegment},
			{"completed", line(NewCompleted(0, 0, OutcomeNoSpeech), 0) + "\n", TypeCompleted},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, dec, err := decodeStream(t, "job", tc.raw)
				if len(got) != 0 {
					t.Fatalf("decoded %d events, want 0 (err = %v)", len(got), err)
				}
				if !errors.Is(err, ErrBeforeAccepted) {
					t.Fatalf("err = %v, want ErrBeforeAccepted", err)
				}
				if !strings.Contains(err.Error(), tc.kind) {
					t.Fatalf("err %q does not name %s", err, tc.kind)
				}
				if dec.Terminal() != "" || dec.Completed() {
					t.Fatalf("Terminal() = %q Completed() = %v", dec.Terminal(), dec.Completed())
				}
			})
		}
	})

	t.Run("refuses a second accepted", func(t *testing.T) {
		second := line(NewAccepted("parakeet", "m", "r", TimingUtterance), 1)
		got, dec, err := decodeStream(t, "job", accepted+"\n"+second+"\n")
		if len(got) != 1 {
			t.Fatalf("decoded %d events, want 1 (err = %v)", len(got), err)
		}
		if !errors.Is(err, ErrDuplicateAccepted) {
			t.Fatalf("err = %v, want ErrDuplicateAccepted", err)
		}
		if dec.Terminal() != "" || dec.Completed() {
			t.Fatalf("Terminal() = %q Completed() = %v", dec.Terminal(), dec.Completed())
		}
	})

	t.Run("failed and cancelled may open the stream", func(t *testing.T) {
		for _, ev := range []Event{
			NewFailed(CodeInternal, "preflight", false),
			NewCancelled(),
		} {
			got, dec, err := decodeStream(t, "job", encodeJSONL(t, "job", ev))
			requireDecoded(t, got, err, 1, "")
			if got[0].Type() != ev.Type() || got[0].SeqOf() != 0 {
				t.Fatalf("got type %q seq %d, want %s seq 0", got[0].Type(), got[0].SeqOf(), ev.Type())
			}
			if dec.Terminal() != ev.Type() || dec.Completed() {
				t.Fatalf("Terminal() = %q Completed() = %v", dec.Terminal(), dec.Completed())
			}
		}
	})

	t.Run("accepted is recorded only after a validated event", func(t *testing.T) {
		progress := line(NewProgress(1, 2), 0)
		done := line(NewCompleted(0, 0, OutcomeNoSpeech), 1)
		dec := NewDecoder(strings.NewReader(strings.Join([]string{progress, accepted, done}, "\n")+"\n"), "job")

		if ev, err := dec.Next(); err == nil || !errors.Is(err, ErrBeforeAccepted) {
			t.Fatalf("first progress: %T %v, want ErrBeforeAccepted", ev, err)
		}
		if dec.Completed() || dec.Terminal() != "" {
			t.Fatalf("rejected progress mutated terminal state: Terminal() = %q", dec.Terminal())
		}
		if ev, err := dec.Next(); err != nil || ev.Type() != TypeAccepted {
			t.Fatalf("accepted after rejected progress: %T %v", ev, err)
		}
		if ev, err := dec.Next(); err != nil || ev.Type() != TypeCompleted {
			t.Fatalf("completed after recovered accepted: %T %v", ev, err)
		}
		if !dec.Completed() {
			t.Fatal("Completed() = false after accepted then completed")
		}
	})

	t.Run("rejected second accepted does not consume seq", func(t *testing.T) {
		dup := line(NewAccepted("parakeet", "m", "r", TimingUtterance), 1)
		done := line(NewCompleted(0, 0, OutcomeNoSpeech), 1)
		dec := NewDecoder(strings.NewReader(strings.Join([]string{accepted, dup, done}, "\n")+"\n"), "job")

		if ev, err := dec.Next(); err != nil || ev.Type() != TypeAccepted {
			t.Fatalf("first accepted: %T %v", ev, err)
		}
		if ev, err := dec.Next(); err == nil || !errors.Is(err, ErrDuplicateAccepted) {
			t.Fatalf("second accepted: %T %v, want ErrDuplicateAccepted", ev, err)
		}
		if ev, err := dec.Next(); err != nil || ev.Type() != TypeCompleted {
			t.Fatalf("completed after rejected second accepted: %T %v (duplicate mutated accepted/seq)", ev, err)
		}
		if !dec.Completed() {
			t.Fatal("Completed() = false after recovered completed")
		}
	})
}
