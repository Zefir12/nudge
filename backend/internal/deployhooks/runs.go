package deployhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"time"
)

const (
	// RunsPerHook bounds the runs kept per webhook; older ones are dropped.
	RunsPerHook = 50
	// MaxRuns bounds the runs of all webhooks together.
	MaxRuns = 10000
	// TriggerLimit triggers per webhook are accepted in TriggerWindow; more answer
	// ErrRateLimited.
	TriggerLimit = 10
	// TriggerWindow is the period TriggerLimit counts in.
	TriggerWindow = time.Minute
)

var (
	// ErrRateLimited means the webhook was triggered TriggerLimit times this window.
	ErrRateLimited = errors.New("the webhook was triggered too often")
	// ErrClosed means the Service is shutting down.
	ErrClosed = errors.New("shutting down")
)

// Status is where a run is.
type Status string

const (
	// StatusQueued waits for the webhook's run before it.
	StatusQueued Status = "queued"
	// StatusRunning is deploying now.
	StatusRunning Status = "running"
	// StatusUpdated replaced the target with a newer image.
	StatusUpdated Status = "updated"
	// StatusUnchanged found nothing newer to pull.
	StatusUnchanged Status = "unchanged"
	// StatusSkipped was a package event the tag filter didn't match.
	StatusSkipped Status = "skipped"
	// StatusFailed says why in Run.Error.
	StatusFailed Status = "failed"
)

// Finished reports whether a run in status s is over.
func (s Status) Finished() bool { return s != StatusQueued && s != StatusRunning }

// Run is one deploy of a webhook.
type Run struct {
	ID         int64
	Hook       string
	Trigger    Trigger
	Status     Status
	QueuedAt   time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	// ImageBefore and ImageAfter are the image IDs the target ran; for a stack the
	// changed services' as "service=ID" lists.
	ImageBefore string
	ImageAfter  string
	// Error says why a run failed, in words safe to show: never a daemon's message.
	Error string
}

// Outcome is what a deploy did.
type Outcome struct {
	// Status is StatusUpdated, StatusUnchanged or StatusFailed.
	Status      Status
	ImageBefore string
	ImageAfter  string
	// Error is a safe message for a failure.
	Error string
}

// DeployFunc updates a webhook's target. ctx ends at Close.
type DeployFunc func(ctx context.Context, h Hook) Outcome

// active is a run that is running or queued, in memory.
type active struct {
	id      int64
	hook    Hook
	deploy  DeployFunc
	done    chan struct{}
	trigger Trigger
}

// slot is a webhook's runs and trigger rate.
type slot struct {
	running *active
	// next is the follow-up queued during running; later triggers merge into it.
	next        *active
	windowStart time.Time
	count       int
}

// idle reports whether the slot holds nothing worth keeping at now.
func (sl *slot) idle(now time.Time) bool {
	return sl.running == nil && !now.Before(sl.windowStart.Add(TriggerWindow))
}

// Ticket is a trigger's run: the one it started or the queued one it merged into.
type Ticket struct {
	RunID int64
	done  <-chan struct{}
}

// Done is closed when the run is over.
func (t Ticket) Done() <-chan struct{} { return t.done }

// allowLocked counts a trigger of hook id, unless it reached TriggerLimit this window;
// it returns the webhook's slot. The caller holds mu.
func (s *Service) allowLocked(id string, now time.Time) (*slot, time.Duration, bool) {
	sl, ok := s.slots[id]
	if !ok {
		sl = &slot{windowStart: now}
		s.pruneLocked(now)
		s.slots[id] = sl
	}
	if !now.Before(sl.windowStart.Add(TriggerWindow)) {
		sl.windowStart, sl.count = now, 0
	}
	if sl.count >= TriggerLimit {
		return sl, sl.windowStart.Add(TriggerWindow).Sub(now), false
	}
	sl.count++
	return sl, 0, true
}

// pruneLocked drops idle slots. Slots exist only for authenticated webhooks, which the
// database bounds, so this only keeps the map from growing with deleted ones.
func (s *Service) pruneLocked(now time.Time) {
	for id, sl := range s.slots {
		if sl.idle(now) {
			delete(s.slots, id)
		}
	}
}

// RateLimitedError is ErrRateLimited with the time until the next trigger is accepted.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return ErrRateLimited.Error() }
func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// Trigger starts a run of h with deploy, or queues one if a run is under way; if one is
// queued already the trigger merges into it. The run outlives ctx, which bounds only
// the bookkeeping.
func (s *Service) Trigger(ctx context.Context, h Hook, trigger Trigger, deploy DeployFunc) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return Ticket{}, ErrClosed
	}
	now := s.clock.Now()
	sl, wait, ok := s.allowLocked(h.ID, now)
	if !ok {
		return Ticket{}, &RateLimitedError{RetryAfter: wait}
	}
	s.touch(ctx, h.ID, now)
	if sl.next != nil {
		return Ticket{RunID: sl.next.id, done: sl.next.done}, nil
	}
	id, err := s.insertRun(ctx, h.ID, trigger, StatusQueued, now)
	if err != nil {
		return Ticket{}, err
	}
	a := &active{id: id, hook: h, deploy: deploy, done: make(chan struct{}), trigger: trigger}
	if sl.running != nil {
		sl.next = a
	} else {
		sl.running = a
		s.wg.Go(func() { s.loop(sl, a) })
	}
	return Ticket{RunID: id, done: a.done}, nil
}

// Skip records a trigger of h that the tag filter let pass by.
func (s *Service) Skip(ctx context.Context, h Hook, trigger Trigger) (Run, error) {
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return Run{}, ErrClosed
	}
	now := s.clock.Now()
	_, wait, ok := s.allowLocked(h.ID, now)
	s.mu.Unlock()
	if !ok {
		return Run{}, &RateLimitedError{RetryAfter: wait}
	}
	s.touch(ctx, h.ID, now)
	id, err := s.insertRun(ctx, h.ID, trigger, StatusSkipped, now)
	if err != nil {
		return Run{}, err
	}
	return s.Run(ctx, h.ID, id)
}

// touch records the webhook's last trigger; best effort.
func (s *Service) touch(ctx context.Context, id string, now time.Time) {
	if _, err := s.db.ExecContext(ctx, `UPDATE deploy_hooks SET last_triggered_at = ? WHERE id = ?`, now.UnixMilli(), id); err != nil {
		s.log.Warn("webhook last trigger", "hook", id, "err", err)
	}
}

// loop runs a and then the follow-ups queued meanwhile, until none is left.
func (s *Service) loop(sl *slot, a *active) {
	for a != nil {
		s.execute(a)
		s.mu.Lock()
		a, sl.next = sl.next, nil
		sl.running = a
		s.mu.Unlock()
	}
}

// execute runs a's deploy and records the outcome; at shutdown it fails the run
// without starting it.
func (s *Service) execute(a *active) {
	defer close(a.done)
	// The bookkeeping outlives a shutdown, so the run doesn't stay "running".
	ctx := context.WithoutCancel(s.ctx)
	var out Outcome
	exists := true
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deploy_hooks WHERE id = ?)`, a.hook.ID).Scan(&exists); err != nil {
		s.log.Error("webhook run: look up webhook", "hook", a.hook.ID, "err", err)
	}
	switch {
	case !exists:
		// Deleted (with its runs) while this one was queued.
		return
	case s.ctx.Err() != nil:
		out = Outcome{Status: StatusFailed, Error: "nudge shut down before the run started"}
	default:
		start := s.clock.Now().UnixMilli()
		if _, err := s.db.ExecContext(ctx, `UPDATE deploy_hook_runs SET status = 'running', started_at = ? WHERE id = ?`, start, a.id); err != nil {
			s.log.Error("webhook run start", "hook", a.hook.ID, "run", a.id, "err", err)
		}
		out = a.deploy(s.ctx, a.hook)
		if s.ctx.Err() != nil && out.Status == StatusFailed {
			out.Error = "nudge shut down during the run"
		}
	}
	switch out.Status {
	case StatusUpdated, StatusUnchanged, StatusFailed:
	default:
		out = Outcome{Status: StatusFailed, Error: "the deploy ended without a result"}
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE deploy_hook_runs SET status = ?, finished_at = ?, image_before = ?, image_after = ?, error = ? WHERE id = ?`,
		string(out.Status), s.clock.Now().UnixMilli(), out.ImageBefore, out.ImageAfter, out.Error, a.id); err != nil {
		s.log.Error("webhook run finish", "hook", a.hook.ID, "run", a.id, "err", err)
	}
	s.log.Info("webhook run", "hook", a.hook.ID, "name", a.hook.Name, "run", a.id, "trigger", a.trigger, "status", out.Status)
}

// insertRun adds a run and drops the oldest beyond RunsPerHook and MaxRuns.
func (s *Service) insertRun(ctx context.Context, hook string, trigger Trigger, status Status, now time.Time) (id int64, err error) {
	ctx = context.WithoutCancel(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("add webhook run: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var finished any
	if status.Finished() {
		finished = now.UnixMilli()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO deploy_hook_runs (hook_id, trigger_kind, status, queued_at, finished_at) VALUES (?, ?, ?, ?, ?)`,
		hook, string(trigger), string(status), now.UnixMilli(), finished)
	if err != nil {
		return 0, fmt.Errorf("add webhook run: %w", err)
	}
	if id, err = res.LastInsertId(); err != nil {
		return 0, fmt.Errorf("add webhook run: %w", err)
	}
	// Runs under way are the newest, so the bounds never drop them.
	if _, err = tx.ExecContext(ctx, `DELETE FROM deploy_hook_runs WHERE hook_id = ? AND id NOT IN
		(SELECT id FROM deploy_hook_runs WHERE hook_id = ? ORDER BY id DESC LIMIT ?)`, hook, hook, RunsPerHook); err != nil {
		return 0, fmt.Errorf("bound webhook runs: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM deploy_hook_runs WHERE id NOT IN
		(SELECT id FROM deploy_hook_runs ORDER BY id DESC LIMIT ?)`, MaxRuns); err != nil {
		return 0, fmt.Errorf("bound webhook runs: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("add webhook run: %w", err)
	}
	return id, nil
}

// Wait waits up to max for t's run to end and returns it; finished is false if it still
// goes on.
func (s *Service) Wait(ctx context.Context, h Hook, t Ticket, maxWait time.Duration) (run Run, finished bool, err error) {
	select {
	case <-t.done:
		finished = true
	case <-s.clock.After(maxWait):
	case <-s.ctx.Done():
		// Shutting down: answer at once, so the server can stop.
	case <-ctx.Done():
		return Run{}, false, ctx.Err()
	}
	run, err = s.Run(ctx, h.ID, t.RunID)
	return run, finished, err
}

const runColumns = `id, hook_id, trigger_kind, status, queued_at, started_at, finished_at, image_before, image_after, error`

func scanRun(row scanner) (Run, error) {
	var r Run
	var queued int64
	var started, finished sql.NullInt64
	if err := row.Scan(&r.ID, &r.Hook, &r.Trigger, &r.Status, &queued, &started, &finished, &r.ImageBefore, &r.ImageAfter, &r.Error); err != nil {
		return Run{}, err
	}
	r.QueuedAt = time.UnixMilli(queued).UTC()
	if started.Valid {
		t := time.UnixMilli(started.Int64).UTC()
		r.StartedAt = &t
	}
	if finished.Valid {
		t := time.UnixMilli(finished.Int64).UTC()
		r.FinishedAt = &t
	}
	return r, nil
}

// Run returns run id of webhook hook.
func (s *Service) Run(ctx context.Context, hook string, id int64) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM deploy_hook_runs WHERE hook_id = ? AND id = ?`, hook, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("get webhook run: %w", err)
	}
	return r, nil
}

// Runs returns a webhook's runs, newest first, if actor may see it.
func (s *Service) Runs(ctx context.Context, actor Actor, env int64, id string) ([]Run, error) {
	if _, err := s.Get(ctx, actor, env, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM deploy_hook_runs WHERE hook_id = ? ORDER BY id DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("list webhook runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list webhook runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list webhook runs: %w", err)
	}
	return out, nil
}

// FailStale marks the runs a previous process left queued or running as failed; call
// it at start, before any trigger.
func (s *Service) FailStale(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE deploy_hook_runs SET status = 'failed', finished_at = ?, error = ?
		WHERE status IN ('queued', 'running')`, s.clock.Now().UnixMilli(), "nudge restarted during the run"); err != nil {
		return fmt.Errorf("fail stale webhook runs: %w", err)
	}
	return nil
}

// Close stops the runs: the running deploys see their context end, queued ones fail
// without starting. It waits for them and refuses new triggers.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// Skipped reports whether a trigger is passed by because of h's tag filter: only a
// signed Forgejo or Gitea package event whose version doesn't match it is.
func Skipped(h Hook, trigger Trigger, header http.Header, body []byte) bool {
	if h.TagFilter == "" || trigger != TriggerSignature {
		return false
	}
	event := header.Get("X-Forgejo-Event")
	if event == "" {
		event = header.Get("X-Gitea-Event")
	}
	if event != "package" {
		return false
	}
	var ev struct {
		Package struct {
			Version string `json:"version"`
		} `json:"package"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		// A package event without a version can't match.
		return true
	}
	ok, err := path.Match(h.TagFilter, ev.Package.Version)
	return err != nil || !ok
}
