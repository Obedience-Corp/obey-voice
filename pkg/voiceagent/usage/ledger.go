// Package usage owns installation-wide worker accounting. Currency is integer
// micro-USD; observations from non-authoritative adapters never settle charges.
package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Policy struct {
	ID                  string
	Revision            int64
	PaidEnabled, Strict bool
	DailyMicroUSD       *int64
	Timezone            string
}
type Request struct {
	ID, Account, PolicyID string
	PolicyRevision        int64
	UpperMicroUSD         *int64
	AllowUncapped         bool
	Now                   time.Time
}
type Reservation struct {
	ID, Account, PolicyID, Bucket, State string
	UpperMicroUSD, ActualMicroUSD        *int64
	Created                              bool
}
type Snapshot struct {
	PolicyID, Bucket                                 string
	DailyMicroUSD, ReservedMicroUSD, SettledMicroUSD *int64
	UnknownCount                                     int
	Frozen                                           bool
}
type Ledger struct{ db *sql.DB }

var ErrOverrun = errors.New("authoritative charge exceeded reserved bound; strict dispatch frozen")

func Open(path string) (*Ledger, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return OpenContext(ctx, path)
}

func OpenContext(ctx context.Context, path string) (*Ledger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("ledger path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open ledger file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	// These pragmas apply to every pooled connection, including replacements.
	q.Add("_pragma", "busy_timeout(3000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	l := &Ledger{db: db}
	err = l.immediate(ctx, func(c *sql.Conn) error {
		for _, query := range []string{
			`CREATE TABLE IF NOT EXISTS policies (id TEXT PRIMARY KEY, revision INTEGER NOT NULL, paid_enabled INTEGER NOT NULL, strict INTEGER NOT NULL, daily_micro INTEGER, timezone TEXT NOT NULL, frozen INTEGER NOT NULL DEFAULT 0, bucket TEXT NOT NULL DEFAULT '', period_end INTEGER NOT NULL DEFAULT 0)`,
			`CREATE TABLE IF NOT EXISTS reservations (id TEXT PRIMARY KEY, account TEXT NOT NULL, policy_id TEXT NOT NULL, policy_revision INTEGER NOT NULL, bucket TEXT NOT NULL, state TEXT NOT NULL, upper_micro INTEGER, actual_micro INTEGER, allow_uncapped INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS reservations_policy ON reservations(policy_id, state, bucket)`,
		} {
			if _, err := c.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize usage ledger: %w", err)
	}
	return l, nil
}
func (l *Ledger) Close() error { return l.db.Close() }

// Every operation, including reads that advance the day, uses one pinned
// connection. A failed commit never becomes permission to dispatch.
func (l *Ledger) immediate(ctx context.Context, fn func(*sql.Conn) error) error {
	c, err := l.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = c.ExecContext(cleanup, "ROLLBACK")
	}()
	if err := fn(c); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	return err
}

type storedPolicy struct {
	Policy
	Frozen    bool
	Bucket    string
	PeriodEnd int64
}

func readPolicy(ctx context.Context, c *sql.Conn, id string) (storedPolicy, error) {
	var p storedPolicy
	var daily sql.NullInt64
	p.ID = id
	err := c.QueryRowContext(ctx, `SELECT revision,paid_enabled,strict,daily_micro,timezone,frozen,bucket,period_end FROM policies WHERE id=?`, id).Scan(&p.Revision, &p.PaidEnabled, &p.Strict, &daily, &p.Timezone, &p.Frozen, &p.Bucket, &p.PeriodEnd)
	p.DailyMicroUSD = nullPtr(daily)
	return p, err
}
func (l *Ledger) SyncPolicy(ctx context.Context, p Policy) error {
	if p.ID == "" || p.Revision < 0 || p.Timezone == "" {
		return errors.New("invalid policy identity or timezone")
	}
	if p.DailyMicroUSD != nil && *p.DailyMicroUSD <= 0 {
		return errors.New("daily allowance must be positive")
	}
	if p.Strict && p.DailyMicroUSD == nil {
		return errors.New("strict policy requires a daily allowance")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return err
	}
	return l.immediate(ctx, func(c *sql.Conn) error {
		old, err := readPolicy(ctx, c, p.ID)
		if errors.Is(err, sql.ErrNoRows) {
			_, err := c.ExecContext(ctx, `INSERT INTO policies(id,revision,paid_enabled,strict,daily_micro,timezone) VALUES(?,?,?,?,?,?)`, p.ID, p.Revision, p.PaidEnabled, p.Strict, p.DailyMicroUSD, p.Timezone)
			return err
		}
		if err != nil {
			return err
		}
		if p.Revision < old.Revision {
			return errors.New("stale policy revision")
		}
		if p.Revision == old.Revision {
			if p.PaidEnabled != old.PaidEnabled || p.Strict != old.Strict || !equalAmount(p.DailyMicroUSD, old.DailyMicroUSD) || p.Timezone != old.Timezone {
				return errors.New("policy revision conflict")
			}
			return nil
		}
		// Keep the active bucket/end and any capability freeze. The requested
		// timezone takes effect only when the existing period ends.
		_, err = c.ExecContext(ctx, `UPDATE policies SET revision=?,paid_enabled=?,strict=?,daily_micro=?,timezone=? WHERE id=?`, p.Revision, p.PaidEnabled, p.Strict, p.DailyMicroUSD, p.Timezone, p.ID)
		return err
	})
}
func advancePeriod(ctx context.Context, c *sql.Conn, p *storedPolicy, now time.Time) error {
	if now.IsZero() {
		return errors.New("accounting time is required")
	}
	if p.Bucket != "" && now.Unix() < p.PeriodEnd {
		return nil
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return err
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	// Changing westward must not reopen a part of the period already charged.
	if p.PeriodEnd > start.Unix() {
		start = time.Unix(p.PeriodEnd, 0)
	}
	p.Bucket = start.UTC().Format(time.RFC3339)
	p.PeriodEnd = end.Unix()
	_, err = c.ExecContext(ctx, `UPDATE policies SET bucket=?,period_end=? WHERE id=?`, p.Bucket, p.PeriodEnd, p.ID)
	return err
}
func readReservation(ctx context.Context, c *sql.Conn, id string) (Reservation, int64, bool, error) {
	var r Reservation
	var rev int64
	var uncapped bool
	var upper, actual sql.NullInt64
	err := c.QueryRowContext(ctx, `SELECT id,account,policy_id,bucket,state,upper_micro,actual_micro,policy_revision,allow_uncapped FROM reservations WHERE id=?`, id).Scan(&r.ID, &r.Account, &r.PolicyID, &r.Bucket, &r.State, &upper, &actual, &rev, &uncapped)
	r.UpperMicroUSD, r.ActualMicroUSD = nullPtr(upper), nullPtr(actual)
	return r, rev, uncapped, err
}
func accounting(ctx context.Context, c *sql.Conn, p storedPolicy) (Snapshot, error) {
	s := Snapshot{PolicyID: p.ID, Bucket: p.Bucket, DailyMicroUSD: p.DailyMicroUSD, Frozen: p.Frozen}
	var reserved, settled int64
	// Unresolved reservations from ALL dates remain obligations. Only settled
	// charges belong exclusively to their original dated bucket.
	err := c.QueryRowContext(ctx, `SELECT COALESCE(SUM(upper_micro),0),COUNT(CASE WHEN upper_micro IS NULL THEN 1 END) FROM reservations WHERE policy_id=? AND state IN ('reserved','dispatched','uncertain')`, p.ID).Scan(&reserved, &s.UnknownCount)
	if err != nil {
		return s, err
	}
	err = c.QueryRowContext(ctx, `SELECT COALESCE(SUM(actual_micro),0) FROM reservations WHERE policy_id=? AND bucket=? AND state='settled'`, p.ID, p.Bucket).Scan(&settled)
	if err != nil {
		return s, err
	}
	s.ReservedMicroUSD, s.SettledMicroUSD = &reserved, &settled
	return s, nil
}
func (l *Ledger) Reserve(ctx context.Context, r Request) (Reservation, error) {
	var out Reservation
	if r.ID == "" || r.PolicyID == "" || r.Account == "" || r.PolicyRevision < 0 || (r.UpperMicroUSD != nil && *r.UpperMicroUSD < 0) {
		return out, errors.New("invalid request")
	}
	err := l.immediate(ctx, func(c *sql.Conn) error {
		existing, _, uncapped, err := readReservation(ctx, c, r.ID)
		if err == nil {
			if existing.Account != r.Account || existing.PolicyID != r.PolicyID || !equalAmount(existing.UpperMicroUSD, r.UpperMicroUSD) || uncapped != r.AllowUncapped {
				return errors.New("request ID reused with different identity or bound")
			}
			out = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p, err := readPolicy(ctx, c, r.PolicyID)
		if err != nil {
			return err
		}
		if !p.PaidEnabled || p.Revision != r.PolicyRevision {
			return errors.New("paid work disabled or policy revision changed")
		}
		if p.Strict && (p.Frozen || r.UpperMicroUSD == nil) {
			return errors.New("strict charge cap unavailable or frozen")
		}
		if r.UpperMicroUSD == nil && !r.AllowUncapped {
			return errors.New("uncapped request requires explicit opt-in")
		}
		if err := advancePeriod(ctx, c, &p, r.Now); err != nil {
			return err
		}
		s, err := accounting(ctx, c, p)
		if err != nil {
			return err
		}
		if p.Strict && s.UnknownCount > 0 {
			return errors.New("strict allowance unavailable while unresolved charges are unknown")
		}
		if r.UpperMicroUSD != nil && p.DailyMicroUSD != nil {
			remaining := *p.DailyMicroUSD
			// Subtractions avoid an overflowing addition of charges and reservations.
			for _, amount := range []int64{*s.SettledMicroUSD, *s.ReservedMicroUSD, *r.UpperMicroUSD} {
				if amount > remaining {
					return errors.New("daily allowance exhausted")
				}
				remaining -= amount
			}
		}
		_, err = c.ExecContext(ctx, `INSERT INTO reservations VALUES(?,?,?,?,?,?,?,?,?,?)`, r.ID, r.Account, r.PolicyID, r.PolicyRevision, p.Bucket, "reserved", r.UpperMicroUSD, nil, r.AllowUncapped, r.Now.Unix())
		if err == nil {
			out = Reservation{ID: r.ID, Account: r.Account, PolicyID: r.PolicyID, Bucket: p.Bucket, State: "reserved", UpperMicroUSD: cloneAmount(r.UpperMicroUSD), Created: true}
		}
		return err
	})
	return out, err
}
func (l *Ledger) MarkDispatched(ctx context.Context, id string, rev int64) error {
	return l.immediate(ctx, func(c *sql.Conn) error {
		r, originalRev, uncapped, err := readReservation(ctx, c, id)
		if err != nil {
			return err
		}
		if r.State != "reserved" || originalRev != rev {
			return errors.New("reservation is not dispatchable")
		}
		p, err := readPolicy(ctx, c, r.PolicyID)
		if err != nil {
			return err
		}
		if !p.PaidEnabled || p.Revision != rev {
			return errors.New("paid work disabled or policy revision changed")
		}
		if p.Strict && (p.Frozen || r.UpperMicroUSD == nil) {
			return errors.New("strict charge cap unavailable or frozen")
		}
		if r.UpperMicroUSD == nil && !uncapped {
			return errors.New("uncapped dispatch not authorized")
		}
		_, err = c.ExecContext(ctx, `UPDATE reservations SET state='dispatched' WHERE id=?`, id)
		return err
	})
}
func (l *Ledger) Settle(ctx context.Context, id string, actual int64) error {
	if actual < 0 {
		return errors.New("negative authoritative charge")
	}
	overrun := false
	err := l.immediate(ctx, func(c *sql.Conn) error {
		r, _, _, err := readReservation(ctx, c, id)
		if err != nil {
			return err
		}
		if r.State == "settled" && r.ActualMicroUSD != nil && *r.ActualMicroUSD == actual {
			return nil
		}
		if r.State != "dispatched" && r.State != "uncertain" {
			return errors.New("reservation is not dispatched")
		}
		if _, err := c.ExecContext(ctx, `UPDATE reservations SET state='settled',actual_micro=? WHERE id=?`, actual, id); err != nil {
			return err
		}
		if r.UpperMicroUSD != nil && actual > *r.UpperMicroUSD {
			overrun = true
			_, err = c.ExecContext(ctx, `UPDATE policies SET frozen=1 WHERE id=?`, r.PolicyID)
		}
		return err
	})
	if err != nil {
		return err
	}
	if overrun {
		return ErrOverrun
	}
	return nil
}
func (l *Ledger) MarkUncertain(ctx context.Context, id string) error {
	return l.changeState(ctx, id, "dispatched", "uncertain")
}
func (l *Ledger) Release(ctx context.Context, id string) error {
	return l.changeState(ctx, id, "reserved", "released")
}
func (l *Ledger) changeState(ctx context.Context, id, from, to string) error {
	return l.immediate(ctx, func(c *sql.Conn) error {
		result, err := c.ExecContext(ctx, `UPDATE reservations SET state=? WHERE id=? AND state=?`, to, id, from)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("reservation is not %s", from)
		}
		return nil
	})
}
func (l *Ledger) Snapshot(ctx context.Context, id string, now time.Time) (Snapshot, error) {
	var out Snapshot
	err := l.immediate(ctx, func(c *sql.Conn) error {
		p, err := readPolicy(ctx, c, id)
		if err != nil {
			return err
		}
		if err := advancePeriod(ctx, c, &p, now); err != nil {
			return err
		}
		out, err = accounting(ctx, c, p)
		return err
	})
	return out, err
}
func nullPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
func equalAmount(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func cloneAmount(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
