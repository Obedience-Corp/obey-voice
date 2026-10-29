package discussion

import (
	"context"
	"errors"
	"testing"
)

func TestDiscussionBoundsAndTicketInvalidation(t *testing.T) {
	s := New(t.Context(), "Request", "all", "invited", []string{"p0", "p1"})
	defer s.End("yielded")
	old := s.Ticket()
	s.Phase("checking", "p0")
	if s.Valid(old) {
		t.Fatal("older ticket survived phase change")
	}
	for range MaxRequests - 1 {
		if err := s.Reserve(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Reserve(); !errors.Is(err, ErrBudget) {
		t.Fatalf("request budget=%v", err)
	}
	s.End("interrupted")
	if s.Valid(s.Ticket()) {
		t.Fatal("ended discussion accepted a ticket")
	}
}
func TestOpenDiscussionExcludesOriginalAndHasSingleReply(t *testing.T) {
	s := New(t.Context(), "Request", "relevant", "open", []string{"p0", "p1"})
	defer s.End("yielded")
	s.Seed(Message{Key: "p0", ResponseID: "original"})
	c, _, err := s.NextContext(nil)
	if err != nil || len(c.Eligible) != 1 || c.Eligible[0] != "p1" || len(c.Delivered) != 1 {
		t.Fatalf("context=%+v err=%v", c, err)
	}
	s.Delivered(Message{Key: "p1", ResponseID: "addition"})
	if _, _, err := s.NextContext(nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("open discussion continued: %v", err)
	}
}
func TestCancellationStopsDiscussionBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := New(ctx, "Request", "all", "invited", []string{"p0", "p1"})
	defer s.End("yielded")
	cancel()
	if err := s.Reserve(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
