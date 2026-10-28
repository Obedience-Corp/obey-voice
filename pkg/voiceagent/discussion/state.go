package discussion

import (
	"context"
	"crypto/rand"
	"slices"
	"sync"
	"time"
)

type State struct {
	seed                          []Message
	mu                            sync.Mutex
	id                            string
	ctx                           context.Context
	cancel                        context.CancelFunc
	phase, mode, coverage, reason string
	members                       []string
	pending                       []string
	counts                        map[string]int
	delivered                     []Message
	candidate                     string
	requests, tail                int
	revision                      uint64
	original                      string
}

func New(ctx context.Context, original, coverage, mode string, members []string) *State {
	limit := 180 * time.Second
	if mode == "open" {
		limit = 30 * time.Second
	}
	child, cancel := context.WithTimeout(ctx, limit)
	pending := []string(nil)
	if coverage == "all" {
		pending = slices.Clone(members)
	}
	state := &State{id: rand.Text(), ctx: child, cancel: cancel, phase: "choosing", mode: mode, coverage: coverage, members: slices.Clone(members), pending: pending, counts: map[string]int{}, original: original, revision: 1, requests: 1}
	state.ctx = WithState(child, state)
	return state
}
func (s *State) ID() string               { return s.id }
func (s *State) Context() context.Context { return s.ctx }
func (s *State) Reserve() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.reason != "" {
		return ErrStale
	}
	requestLimit := MaxRequests
	if s.mode == "open" {
		requestLimit = 4
	}
	if s.requests >= requestLimit {
		return ErrBudget
	}
	s.requests++
	return nil
}
func (s *State) Ticket() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.revision }
func (s *State) Valid(revision uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason == "" && s.revision == revision && s.ctx.Err() == nil
}
func (s *State) Phase(phase, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
	s.candidate = key
	s.revision++
}
func (s *State) End(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reason == "" {
		s.reason = reason
		s.phase = "ended"
		s.revision++
		s.cancel()
	}
}
func (s *State) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{ID: s.id, Revision: s.revision, Phase: s.phase, Mode: s.mode, Members: slices.Clone(s.members), Candidate: s.candidate, Delivered: len(s.delivered), Uncovered: slices.Clone(s.pending), Reason: s.reason}
}
func (s *State) NextContext(participants []Participant) (Context, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return Context{}, "", err
	}
	maxReplies := min(MaxResponses, 2*len(s.members))
	if len(s.members) == 1 || s.mode == "open" {
		maxReplies = 1
	}
	if len(s.delivered) >= maxReplies {
		return Context{}, "", ErrBudget
	}
	eligible := []string{}
	last := ""
	if len(s.seed) > 0 {
		last = s.seed[len(s.seed)-1].Key
	}
	if len(s.delivered) > 0 {
		last = s.delivered[len(s.delivered)-1].Key
	}
	for _, k := range s.members {
		if k != last && s.counts[k] < 2 {
			eligible = append(eligible, k)
		}
	}
	c := Context{Original: s.original, Participants: participants, Eligible: eligible, Delivered: append(slices.Clone(s.seed), s.delivered...), Mode: s.mode, Coverage: s.coverage, Remaining: maxReplies - len(s.delivered)}
	if len(s.pending) > 0 {
		return c, s.pending[0], nil
	}
	if s.tail >= 4 {
		return Context{}, "", ErrBudget
	}
	s.tail++
	return c, "", nil
}
func (s *State) Passed(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = slices.DeleteFunc(s.pending, func(k string) bool { return k == key })
	s.revision++
	return len(s.pending) > 0
}
func (s *State) Delivered(m Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered = append(s.delivered, m)
	s.counts[m.Key]++
	s.pending = slices.DeleteFunc(s.pending, func(k string) bool { return k == m.Key })
	s.revision++
}
func (s *State) Returning(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[key] > 0
}

type stateKey struct{}

func WithState(ctx context.Context, s *State) context.Context {
	return context.WithValue(ctx, stateKey{}, s)
}
func ReserveRequest(ctx context.Context) error {
	if s, ok := ctx.Value(stateKey{}).(*State); ok {
		return s.Reserve()
	}
	return nil
}

// Bind cancels private/output work when either the caller or the discussion ends.
func Bind(ctx context.Context, s *State) (context.Context, func()) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.Context(), cancel)
	return WithState(child, s), func() { stop(); cancel() }
}

func (s *State) Seed(m Message) { s.mu.Lock(); defer s.mu.Unlock(); s.seed = append(s.seed, m) }
