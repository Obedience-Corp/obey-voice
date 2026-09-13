package usage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const childLedgerMode = "USAGE_LEDGER_CHILD"

func testPolicy() Policy {
	daily := int64(100)
	return Policy{ID: "policy", Revision: 1, PaidEnabled: true, Strict: true, DailyMicroUSD: &daily, Timezone: "UTC"}
}

func openTestLedger(t *testing.T) (*Ledger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SyncPolicy(context.Background(), testPolicy()); err != nil {
		l.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

func TestLedgerChildReserve(t *testing.T) {
	mode := os.Getenv(childLedgerMode)
	if mode == "tx_insert_exit" {
		l, err := Open(os.Getenv("USAGE_LEDGER_DB"))
		if err != nil {
			fmt.Println("error:" + err.Error())
			return
		}
		defer l.Close()
		_ = l.immediate(context.Background(), func(c *sql.Conn) error {
			_, err := c.ExecContext(context.Background(), `INSERT INTO reservations VALUES(?,?,?,?,?,?,?,?,?,?)`, "tx-crash", "account", "policy", 1, "2026-09-13T00:00:00Z", "reserved", 10, nil, false, 1)
			if err != nil {
				return err
			}
			os.Exit(0)
			return nil
		})
		return
	}
	if mode == "tx_cancel_commit" {
		l, err := Open(os.Getenv("USAGE_LEDGER_DB"))
		if err != nil {
			fmt.Println("error:" + err.Error())
			return
		}
		defer l.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err = l.immediate(ctx, func(c *sql.Conn) error {
			if _, err := c.ExecContext(ctx, `INSERT INTO reservations VALUES(?,?,?,?,?,?,?,?,?,?)`, "tx-cancel", "account", "policy", 1, "2026-09-13T00:00:00Z", "reserved", 10, nil, false, 1); err != nil {
				return err
			}
			cancel()
			return nil
		})
		if err != nil {
			fmt.Println("commit-error:" + err.Error())
		}
		return
	}
	if mode != "reserve" {
		t.Skip("subprocess helper")
	}
	for barrier := os.Getenv("USAGE_LEDGER_BARRIER"); barrier != ""; {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	path := os.Getenv("USAGE_LEDGER_DB")
	id := os.Getenv("USAGE_LEDGER_REQUEST")
	l, err := Open(path)
	if err != nil {
		fmt.Println("error:" + err.Error())
		return
	}
	defer l.Close()
	upper := int64(100)
	res, err := l.Reserve(context.Background(), Request{ID: id, Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		fmt.Println("error:" + err.Error())
		return
	}
	if os.Getenv("USAGE_LEDGER_CRASH") == "after_reserve" {
		os.Exit(0)
	}
	if err := l.MarkDispatched(context.Background(), id, 1); err != nil {
		fmt.Println("error:" + err.Error())
		return
	}
	if os.Getenv("USAGE_LEDGER_CRASH") == "after_dispatch" {
		os.Exit(0)
	}
	fmt.Printf("created=%t state=%s\n", res.Created, res.State)
}

func runLedgerBoundaryChild(t *testing.T, path, mode string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestLedgerChildReserve$", "-test.v=false")
	cmd.Env = append(os.Environ(), childLedgerMode+"="+mode, "USAGE_LEDGER_DB="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("boundary child %s failed: %v (%s)", mode, err, out)
	}
}

func TestLedgerCrashInsideTransactionRollsBackInsert(t *testing.T) {
	l, path := openTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	runLedgerBoundaryChild(t, path, "tx_insert_exit")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	upper := int64(10)
	res, err := l.Reserve(context.Background(), Request{ID: "tx-crash", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)})
	if err != nil || !res.Created {
		t.Fatalf("reserve after transaction crash = %+v, %v; want fresh insert after rollback", res, err)
	}
}

func TestLedgerCanceledCommitRollsBackInsert(t *testing.T) {
	l, path := openTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	runLedgerBoundaryChild(t, path, "tx_cancel_commit")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	upper := int64(10)
	res, err := l.Reserve(context.Background(), Request{ID: "tx-cancel", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)})
	if err != nil || !res.Created {
		t.Fatalf("reserve after canceled commit = %+v, %v; want fresh insert after rollback", res, err)
	}
}

func TestLedgerTwoProcessesRaceFinalAllowance(t *testing.T) {
	_, path := openTestLedger(t)
	cmds := make([]*exec.Cmd, 2)
	outs := make([]*bytes.Buffer, 2)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run", "^TestLedgerChildReserve$", "-test.v=false")
		barrier := filepath.Join(filepath.Dir(path), "start-barrier")
		cmds[i].Env = append(os.Environ(), childLedgerMode+"=reserve", "USAGE_LEDGER_DB="+path, "USAGE_LEDGER_REQUEST=r"+strconv.Itoa(i), "USAGE_LEDGER_BARRIER="+barrier)
		outs[i] = new(bytes.Buffer)
		cmds[i].Stdout = outs[i]
	}
	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	barrier := filepath.Join(filepath.Dir(path), "start-barrier")
	if err := os.WriteFile(barrier, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := 0
	for i, cmd := range cmds {
		err := cmd.Wait()
		if err != nil {
			t.Fatalf("reserve child failed: %v (%s)", err, outs[i].String())
		}
		if strings.Contains(outs[i].String(), "created=true") {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("two child reservations created=%d, want exactly one for final allowance", created)
	}
}

func TestLedgerReplayRequestIDDoesNotDoubleReserve(t *testing.T) {
	l, _ := openTestLedger(t)
	upper := int64(40)
	r := Request{ID: "same", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	one, err := l.Reserve(context.Background(), r)
	if err != nil || !one.Created {
		t.Fatalf("first reserve = %+v, %v", one, err)
	}
	two, err := l.Reserve(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if two.Created || two.ID != one.ID || two.State != one.State {
		t.Fatalf("replay = %+v, want existing reservation without creation", two)
	}
	s, err := l.Snapshot(context.Background(), "policy", r.Now)
	if err != nil || s.ReservedMicroUSD == nil || *s.ReservedMicroUSD != 40 {
		t.Fatalf("snapshot = %+v, %v; want one 40-unit reservation", s, err)
	}
}

func TestLedgerDispatchedReservationRemainsAfterRestart(t *testing.T) {
	l, path := openTestLedger(t)
	upper := int64(40)
	r := Request{ID: "crashed", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	if _, err := l.Reserve(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDispatched(context.Background(), r.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	replay, err := l.Reserve(context.Background(), r)
	if err != nil || replay.Created || replay.State != "dispatched" {
		t.Fatalf("restarted replay = %+v, %v; dispatched reservation was lost", replay, err)
	}
	if err := l.MarkUncertain(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	s, err := l.Snapshot(context.Background(), "policy", r.Now)
	if err != nil || s.UnknownCount != 0 {
		t.Fatalf("snapshot after uncertain = %+v, %v; want zero unknown bounds for known upper amount", s, err)
	}
}

func runLedgerCrashChild(t *testing.T, path, id, crash string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestLedgerChildReserve$", "-test.v=false")
	cmd.Env = append(os.Environ(), childLedgerMode+"=reserve", "USAGE_LEDGER_DB="+path, "USAGE_LEDGER_REQUEST="+id, "USAGE_LEDGER_CRASH="+crash)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash child %s failed: %v (%s)", crash, err, out)
	}
}

func TestLedgerCrashAfterReserveLeavesReservationReplayable(t *testing.T) {
	l, path := openTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	runLedgerCrashChild(t, path, "crash-reserve", "after_reserve")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	upper := int64(100)
	replay, err := l.Reserve(context.Background(), Request{ID: "crash-reserve", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)})
	if err != nil || replay.Created || replay.State != "reserved" {
		t.Fatalf("replay after reserve crash = %+v, %v; want retained reserved request", replay, err)
	}
	if err := l.Release(context.Background(), replay.ID); err != nil {
		t.Fatalf("release provably undispatched reservation: %v", err)
	}
}

func TestLedgerCrashAfterDispatchRetainsReservationUntilReconciliation(t *testing.T) {
	l, path := openTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	runLedgerCrashChild(t, path, "crash-dispatch", "after_dispatch")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	upper := int64(100)
	r := Request{ID: "crash-dispatch", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	replay, err := l.Reserve(context.Background(), r)
	if err != nil || replay.Created || replay.State != "dispatched" {
		t.Fatalf("replay after dispatch crash = %+v, %v; want retained dispatched request", replay, err)
	}
	if err := l.Release(context.Background(), replay.ID); err == nil {
		t.Fatal("released dispatched reservation after crash")
	}
	if err := l.MarkUncertain(context.Background(), replay.ID); err != nil {
		t.Fatal(err)
	}
	s, err := l.Snapshot(context.Background(), "policy", r.Now)
	if err != nil || s.ReservedMicroUSD == nil || *s.ReservedMicroUSD != 100 || s.UnknownCount != 0 {
		t.Fatalf("snapshot after dispatch crash = %+v, %v; want known reserved amount and zero unknown bounds", s, err)
	}
}

func TestLedgerStaleRevisionAndPaidDisableRejectReserve(t *testing.T) {
	l, _ := openTestLedger(t)
	updated := testPolicy()
	updated.Revision = 2
	updated.PaidEnabled = false
	if err := l.SyncPolicy(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	upper := int64(1)
	_, err := l.Reserve(context.Background(), Request{ID: "stale", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: time.Now()})
	if err == nil {
		t.Fatal("stale reserve succeeded after policy revision changed")
	}
	_, err = l.Reserve(context.Background(), Request{ID: "off", Account: "account", PolicyID: "policy", PolicyRevision: 2, UpperMicroUSD: &upper, Now: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "paid work disabled") {
		t.Fatalf("paid-off reserve error = %v, want paid work disabled", err)
	}
}

func TestLedgerStorageErrorFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open(directory) succeeded; storage error must fail closed")
	}
}

func TestLedgerUnsettledReservationCarriesAcrossMidnight(t *testing.T) {
	l, _ := openTestLedger(t)
	first := int64(80)
	dayOne := time.Date(2026, 9, 13, 23, 59, 0, 0, time.UTC)
	if _, err := l.Reserve(context.Background(), Request{ID: "before-midnight", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &first, Now: dayOne}); err != nil {
		t.Fatal(err)
	}
	second := int64(30)
	_, err := l.Reserve(context.Background(), Request{ID: "after-midnight", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &second, Now: dayOne.Add(2 * time.Minute)})
	if err == nil || !strings.Contains(err.Error(), "daily allowance exhausted") {
		t.Fatalf("next-period reserve error = %v, want unresolved prior reservation to carry over", err)
	}
}

func TestLedgerTimezoneChangeDoesNotResetCurrentAllowance(t *testing.T) {
	l, _ := openTestLedger(t)
	first := int64(80)
	now := time.Date(2026, 9, 13, 0, 30, 0, 0, time.UTC)
	if _, err := l.Reserve(context.Background(), Request{ID: "before-timezone", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &first, Now: now}); err != nil {
		t.Fatal(err)
	}
	p := testPolicy()
	p.Revision = 2
	p.Timezone = "America/Denver"
	if err := l.SyncPolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	second := int64(30)
	_, err := l.Reserve(context.Background(), Request{ID: "after-timezone", Account: "account", PolicyID: "policy", PolicyRevision: 2, UpperMicroUSD: &second, Now: now})
	if err == nil || !strings.Contains(err.Error(), "daily allowance exhausted") {
		t.Fatalf("timezone-change reserve error = %v, want allowance preserved across policy-period change", err)
	}
}

func TestLedgerSettledAllowanceSurvivesTimezoneChangeWithinPeriod(t *testing.T) {
	l, _ := openTestLedger(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	upper := int64(80)
	r := Request{ID: "settled-timezone", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: now}
	if _, err := l.Reserve(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDispatched(context.Background(), r.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := l.Settle(context.Background(), r.ID, 80); err != nil {
		t.Fatal(err)
	}

	p := testPolicy()
	p.Revision = 2
	p.Timezone = "America/Denver"
	if err := l.SyncPolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	next := int64(30)
	_, err := l.Reserve(context.Background(), Request{ID: "settled-timezone-next", Account: "account", PolicyID: "policy", PolicyRevision: 2, UpperMicroUSD: &next, Now: now.Add(time.Hour)})
	if err == nil {
		t.Fatal("timezone change reset settled allowance before the active period ended")
	}
	if !strings.Contains(err.Error(), "daily allowance") {
		t.Fatalf("reserve after timezone change error = %v, want daily allowance rejection", err)
	}
}

func TestLedgerSettlementOverrunFreezesStrictPolicy(t *testing.T) {
	l, _ := openTestLedger(t)
	upper := int64(40)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	if _, err := l.Reserve(context.Background(), Request{ID: "overrun", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDispatched(context.Background(), "overrun", 1); err != nil {
		t.Fatal(err)
	}
	if err := l.Settle(context.Background(), "overrun", 60); !errors.Is(err, ErrOverrun) {
		t.Fatalf("overrun settlement = %v, want ErrOverrun after retaining charge and freezing policy", err)
	}
	replay, err := l.Reserve(context.Background(), Request{ID: "overrun", Account: "account", PolicyID: "policy", PolicyRevision: 1, UpperMicroUSD: &upper, Now: now})
	if err != nil || replay.Created || replay.ActualMicroUSD == nil || *replay.ActualMicroUSD != 60 {
		t.Fatalf("overrun replay = %+v, %v; want persisted actual charge", replay, err)
	}
	s, err := l.Snapshot(context.Background(), "policy", now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Frozen {
		t.Fatalf("snapshot = %+v, want strict policy frozen after known overrun", s)
	}
}
