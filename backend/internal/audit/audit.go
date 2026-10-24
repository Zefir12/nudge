// Package audit stores the audit log: one entry per write through the API, kept for a
// retention period.
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	// DefaultLimit is the page size of a List call without a limit.
	DefaultLimit = 50
	// MaxLimit bounds the entries one List call returns.
	MaxLimit = 200
	// DefaultRetention is how long entries are kept when Options.Retention is zero.
	DefaultRetention = 90 * 24 * time.Hour
	// DefaultMaxEntries bounds the table when Options.MaxEntries is zero.
	DefaultMaxEntries = 1_000_000
	// maxField bounds every text field, so a long name in a request can't bloat a row.
	maxField = 256
)

// Entry is one recorded write.
type Entry struct {
	ID   int64
	Time time.Time
	// Username is who made the request; empty for public routes such as setup.
	Username string
	// Token is the name of the API token the request used, empty for a session.
	Token string
	// Environment is the environment the route names, if any.
	Environment string
	// Action is the route pattern, e.g. "POST /api/v1/environments/{env}/containers/{id}/start".
	Action string
	// Target is the object acted on: an ID from the path or a name from the body.
	Target string
	// Status is the HTTP status the API answered.
	Status int
}

// Failed reports whether the write was refused or failed.
func (e Entry) Failed() bool { return e.Status >= 400 }

// Query selects entries, newest first. Zero fields don't filter.
type Query struct {
	// Before returns only entries with a smaller ID: the cursor for the next page.
	Before int64
	// Limit is the page size, DefaultLimit when zero, at most MaxLimit.
	Limit       int
	Username    string
	Environment string
	// Search matches the action or the target, case-insensitively.
	Search string
	// Failed, when set, returns only failed (true) or only successful (false) writes.
	Failed *bool
	// Since and Until bound the time, inclusive and exclusive.
	Since, Until time.Time
}

// Options are the Service's dependencies.
type Options struct {
	DB  *sql.DB
	Log *slog.Logger
	// Now is the clock (time.Now by default).
	Now func() time.Time
	// Retention is how long entries are kept (DefaultRetention by default).
	Retention time.Duration
	// MaxEntries is how many of the newest entries Prune keeps at most
	// (DefaultMaxEntries by default).
	MaxEntries int
}

// Service records and lists audit entries.
type Service struct {
	db        *sql.DB
	log       *slog.Logger
	now       func() time.Time
	retention time.Duration
	max       int
}

// New returns a Service.
func New(opts Options) *Service {
	s := &Service{db: opts.DB, log: opts.Log, now: opts.Now, retention: opts.Retention, max: opts.MaxEntries}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.retention <= 0 {
		s.retention = DefaultRetention
	}
	if s.max <= 0 {
		s.max = DefaultMaxEntries
	}
	return s
}

// Record stores e; its ID is ignored and a zero Time means now.
func (s *Service) Record(ctx context.Context, e Entry) error {
	if e.Time.IsZero() {
		e.Time = s.now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (time, username, token, environment, action, target, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMilli(), clip(e.Username), clip(e.Token), clip(e.Environment), clip(e.Action), clip(e.Target), e.Status)
	if err != nil {
		return fmt.Errorf("record audit entry: %w", err)
	}
	return nil
}

// List returns the entries q selects, newest first, and whether more follow.
func (s *Service) List(ctx context.Context, q Query) ([]Entry, bool, error) {
	var where []string
	var args []any
	add := func(cond string, a ...any) {
		where = append(where, cond)
		args = append(args, a...)
	}
	if q.Before > 0 {
		add("id < ?", q.Before)
	}
	if q.Username != "" {
		add("username = ? COLLATE NOCASE", q.Username)
	}
	if q.Environment != "" {
		add("environment = ?", q.Environment)
	}
	if q.Search != "" {
		like := "%" + escapeLike(q.Search) + "%"
		add(`(action LIKE ? ESCAPE '\' OR target LIKE ? ESCAPE '\')`, like, like)
	}
	if q.Failed != nil {
		if *q.Failed {
			add("status >= 400")
		} else {
			add("status < 400")
		}
	}
	if !q.Since.IsZero() {
		add("time >= ?", q.Since.UnixMilli())
	}
	if !q.Until.IsZero() {
		add("time < ?", q.Until.UnixMilli())
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	query := `SELECT id, time, username, token, environment, action, target, status FROM audit_log`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- constant clauses; values are bound
	}
	query += " ORDER BY id DESC LIMIT ?"
	// One more than a page tells whether there is a next one.
	rows, err := s.db.QueryContext(ctx, query, append(args, limit+1)...)
	if err != nil {
		return nil, false, fmt.Errorf("list audit entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := []Entry{}
	for rows.Next() {
		var e Entry
		var ms int64
		if err := rows.Scan(&e.ID, &ms, &e.Username, &e.Token, &e.Environment, &e.Action, &e.Target, &e.Status); err != nil {
			return nil, false, fmt.Errorf("scan audit entry: %w", err)
		}
		e.Time = time.UnixMilli(ms)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list audit entries: %w", err)
	}
	if len(entries) > limit {
		return entries[:limit], true, nil
	}
	return entries, false, nil
}

// Prune deletes the entries older than the retention period, and the oldest beyond
// MaxEntries, and returns how many.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	cutoff := s.now().Add(-s.retention).UnixMilli()
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM audit_log
		WHERE time < ? OR id <= (SELECT MAX(id) FROM audit_log) - ?`, cutoff, s.max)
	if err != nil {
		return 0, fmt.Errorf("prune audit log: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune audit log: %w", err)
	}
	return n, nil
}

// RunCleanup calls Prune every interval until ctx is cancelled.
func (s *Service) RunCleanup(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.Prune(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Warn("audit log cleanup", "err", err)
				}
				continue
			}
			if n > 0 {
				s.log.Debug("pruned audit log", "count", n)
			}
		}
	}
}

// clip cuts v to maxField bytes, at a rune boundary.
func clip(v string) string {
	if len(v) <= maxField {
		return v
	}
	return strings.ToValidUTF8(v[:maxField], "")
}

func escapeLike(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}
