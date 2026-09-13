package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestLedgerReserveIdempotentAndLifecycle(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	amount := int64(100)
	if err := l.SyncPolicy(ctx, Policy{ID: "workers", Revision: 1, PaidEnabled: true, DailyMicroUSD: ptr(150), Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	r, err := l.Reserve(ctx, Request{ID: "req-1", Account: "acct", PolicyID: "workers", PolicyRevision: 1, UpperMicroUSD: &amount, Now: time.Now()})
	if err != nil || !r.Created {
		t.Fatalf("reserve = %+v, %v", r, err)
	}
	replay, err := l.Reserve(ctx, Request{ID: "req-1", Account: "acct", PolicyID: "workers", PolicyRevision: 1, UpperMicroUSD: &amount, Now: time.Now()})
	if err != nil || replay.Created {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if err := l.MarkDispatched(ctx, "req-1", 1); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDispatched(ctx, "req-1", 1); err == nil {
		t.Fatal("duplicate dispatch accepted")
	}
	if err := l.Settle(ctx, "req-1", 90); err != nil {
		t.Fatal(err)
	}
	s, err := l.Snapshot(ctx, "workers", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.SettledMicroUSD == nil || *s.SettledMicroUSD != 90 {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestLedgerPolicyAndAllowanceBoundaries(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	if err := l.SyncPolicy(ctx, Policy{ID: "p", Revision: 2, PaidEnabled: false, Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ctx, Request{ID: "x", Account: "a", PolicyID: "p", PolicyRevision: 2, Now: time.Now()}); err == nil {
		t.Fatal("paid disabled reservation accepted")
	}
	if err := l.SyncPolicy(ctx, Policy{ID: "p", Revision: 1, PaidEnabled: false, Timezone: "UTC"}); err == nil {
		t.Fatal("stale policy accepted")
	}
}

func ptr(v int64) *int64 { return &v }
