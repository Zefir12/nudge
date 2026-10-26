package deployments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"
)

const (
	// RunsPerDeployment bounds the runs kept per deployment; older ones are dropped.
	RunsPerDeployment = 100
	// MaxRuns bounds the runs of all deployments together.
	MaxRuns = 10000
	// maxValue bounds a variable's value.
	maxValue = 512
)

// ErrClosed means the Service is shutting down.
var ErrClosed = errors.New("shutting down")

// Status is where a run is.
type Status string

const (
	// StatusQueued waits for the run before it.
	StatusQueued Status = "queued"
	// StatusRunning is deploying now.
	StatusRunning Status = "running"
	// StatusSucceeded deployed the stack.
	StatusSucceeded Status = "succeeded"
	// StatusFailed says why in Run.Error.
	StatusFailed Status = "failed"
	// StatusSuperseded was queued and replaced by a newer request before it started.
	StatusSuperseded Status = "superseded"
	// StatusRolledBack failed and redeployed what ran before.
	StatusRolledBack Status = "rolled_back"
)

// Finished reports whether a run in status s is over.
func (s Status) Finished() bool { return s != StatusQueued && s != StatusRunning }

// Actor kinds of a run.
const (
	ActorUser  = "user"
	ActorToken = "token"
)

// Run is one deploy request.
type Run struct {
	ID         int64
	Deployment int64
	// ActorKind is ActorUser or ActorToken, ActorName the username or token name.
	ActorKind string
	ActorName string
	Status    Status
	// Variables are the values the run sets; Previous the ones they replaced, once it
	// ran ("" for a variable the stack didn't have).
	Variables  map[string]string
	Previous   map[string]string
	QueuedAt   time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	// ImageBefore and ImageAfter are the changed services' images as "service=ID" lists.
	ImageBefore string
	ImageAfter  string
	// Error says why a run failed, in words safe to show: never a daemon's message.
	Error string
}

// Request is a deploy someone asked for.
type Request struct {
	ActorKind string
	ActorName string
	Variables map[string]string
}

// Outcome is what a deploy did.
type Outcome struct {
	// Status is StatusSucceeded, StatusFailed or StatusRolledBack.
	Status      Status
	Previous    map[string]string
	ImageBefore string
	ImageAfter  string
	// Error is a safe message for a failure.
	Error string
}

// DeployFunc deploys run r of d. ctx ends at Close.
type DeployFunc func(ctx context.Context, d Deployment, r Run) Outcome

// active is a run that is running or queued, in memory.
type active struct {
	run    Run
	d      Deployment
	deploy DeployFunc
	done   chan struct{}
}

// slot is a deployment's run under way and the one queued after it.
type slot struct {
	running *active
	next    *active
}

// Ticket is a request's run.
type Ticket struct {
	RunID int64
	done  <-chan struct{}
}

// Done is closed when the run is over.
func (t Ticket) Done() <-chan struct{} { return t.done }

// valuePattern is what a variable's value may hold: image references and tags, never
// the "$" of an interpolation, quotes, spaces or line breaks.
var valuePattern = regexp.MustCompile(`^[A-Za-z0-9._:/@+=-]+$`)

func validVariables(d Deployment, vars map[string]string) error {
	for k, v := range vars {
		if !slices.Contains(d.Variables, k) {
			return &InputError{Msg: fmt.Sprintf("variable %q isn't one this deployment lets deploys set", k)}
		}
		if len(v) > maxValue || !valuePattern.MatchString(v) {
			return &InputError{Msg: fmt.Sprintf("variable %q: the value must be 1 to %d letters, digits or ._:/@+=-", k, maxValue)}
		}
	}
	return nil
}

// Deploy queues a run of d that deploy carries out. Runs of a deployment go one at a
// time; one request waits behind the running one, and a newer request replaces it (the
// replaced run ends superseded). The run outlives ctx, which bounds the bookkeeping.
func (s *Service) Deploy(ctx context.Context, d Deployment, req Request, deploy DeployFunc) (Ticket, error) {
	if req.Variables == nil {
		req.Variables = map[string]string{}
	}
	if err := validVariables(d, req.Variables); err != nil {
		return Ticket{}, err
	}
	s.mu.Lock()
	closed := s.closed || s.ctx.Err() != nil
	s.mu.Unlock()
	if closed {
		return Ticket{}, ErrClosed
	}
	now := s.clock.Now()
	run := Run{Deployment: d.ID, ActorKind: req.ActorKind, ActorName: req.ActorName, Status: StatusQueued, Variables: req.Variables, Previous: map[string]string{}, QueuedAt: now.UTC()}
	id, err := s.insertRun(ctx, run)
	if err != nil {
		return Ticket{}, err
	}
	run.ID = id
	a := &active{run: run, d: d, deploy: deploy, done: make(chan struct{})}

	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		s.finish(context.WithoutCancel(ctx), a, Outcome{Status: StatusFailed, Error: "nudge shut down before the run started"})
		close(a.done)
		return Ticket{}, ErrClosed
	}
	sl := s.slots[d.ID]
	if sl == nil {
		sl = &slot{}
		s.slots[d.ID] = sl
	}
	var replaced *active
	if sl.running == nil {
		sl.running = a
		s.wg.Go(func() { s.loop(d.ID, sl, a) })
	} else {
		replaced, sl.next = sl.next, a
		if replaced != nil {
			// The replaced request's values still land, unless this one sets them too.
			merged := maps.Clone(replaced.run.Variables)
			maps.Copy(merged, a.run.Variables)
			a.run.Variables = merged
		}
	}
	vars := a.run.Variables
	s.mu.Unlock()
	if replaced != nil {
		bg := context.WithoutCancel(ctx)
		if b, err := json.Marshal(vars); err == nil {
			if _, err := s.db.ExecContext(bg, `UPDATE deployment_runs SET variables = ? WHERE id = ?`, string(b), id); err != nil {
				s.log.Error("deployment run: merge variables", "deployment", d.ID, "run", id, "err", err)
			}
		}
		s.finish(bg, replaced, Outcome{Status: StatusSuperseded, Error: fmt.Sprintf("replaced by run %d before it started; its variables went into that run", id)})
		close(replaced.done)
	}
	return Ticket{RunID: id, done: a.done}, nil
}

// loop runs a and then the run queued meanwhile, until none is left.
func (s *Service) loop(id int64, sl *slot, a *active) {
	for a != nil {
		s.execute(a)
		s.mu.Lock()
		a, sl.next = sl.next, nil
		sl.running = a
		if a == nil {
			delete(s.slots, id)
		}
		s.mu.Unlock()
	}
}

// execute runs a's deploy and records the outcome; at shutdown it fails the run
// without starting it.
func (s *Service) execute(a *active) {
	defer close(a.done)
	// The bookkeeping outlives a shutdown, so the run doesn't stay "running".
	ctx := context.WithoutCancel(s.ctx)
	exists := true
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deployments WHERE id = ?)`, a.d.ID).Scan(&exists); err != nil {
		s.log.Error("deployment run: look up deployment", "deployment", a.d.ID, "err", err)
	}
	if !exists {
		// Deleted (with its runs) while this one was queued.
		return
	}
	if s.ctx.Err() != nil {
		s.finish(ctx, a, Outcome{Status: StatusFailed, Error: "nudge shut down before the run started"})
		return
	}
	start := s.clock.Now()
	if _, err := s.db.ExecContext(ctx, `UPDATE deployment_runs SET status = 'running', started_at = ? WHERE id = ?`, start.UnixMilli(), a.run.ID); err != nil {
		s.log.Error("deployment run start", "deployment", a.d.ID, "run", a.run.ID, "err", err)
	}
	a.run.Status, a.run.StartedAt = StatusRunning, &start
	out := a.deploy(s.ctx, a.d, a.run)
	if s.ctx.Err() != nil && out.Status == StatusFailed {
		out.Error = "nudge shut down during the run"
	}
	switch out.Status {
	case StatusSucceeded, StatusFailed, StatusRolledBack:
	default:
		out = Outcome{Status: StatusFailed, Error: "the deploy ended without a result"}
	}
	s.finish(ctx, a, out)
}

// finish records a's outcome.
func (s *Service) finish(ctx context.Context, a *active, out Outcome) {
	prev := out.Previous
	if prev == nil {
		prev = map[string]string{}
	}
	previous, err := json.Marshal(prev)
	if err != nil {
		previous = []byte("{}")
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE deployment_runs SET status = ?, finished_at = ?, previous = ?, image_before = ?, image_after = ?, error = ? WHERE id = ?`,
		string(out.Status), s.clock.Now().UnixMilli(), string(previous), out.ImageBefore, out.ImageAfter, out.Error, a.run.ID); err != nil {
		s.log.Error("deployment run finish", "deployment", a.d.ID, "run", a.run.ID, "err", err)
	}
	s.log.Info("deployment run", "deployment", a.d.Name, "run", a.run.ID, "actor", a.run.ActorKind+":"+a.run.ActorName, "status", out.Status)
}

// insertRun adds a queued run and drops the oldest beyond RunsPerDeployment and MaxRuns.
func (s *Service) insertRun(ctx context.Context, r Run) (id int64, err error) {
	ctx = context.WithoutCancel(ctx)
	vars, err := json.Marshal(r.Variables)
	if err != nil {
		return 0, fmt.Errorf("add deployment run: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("add deployment run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// A deployment deleted since it was authorized has no row to add a run to.
	res, err := tx.ExecContext(ctx, `INSERT INTO deployment_runs (deployment_id, actor_kind, actor_name, status, variables, queued_at)
		SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM deployments WHERE id = ?)`,
		r.Deployment, r.ActorKind, r.ActorName, string(r.Status), string(vars), r.QueuedAt.UnixMilli(), r.Deployment)
	if err != nil {
		return 0, fmt.Errorf("add deployment run: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return 0, ErrNotFound
	}
	if id, err = res.LastInsertId(); err != nil {
		return 0, fmt.Errorf("add deployment run: %w", err)
	}
	// Runs under way are the newest, so the bounds never drop them.
	if _, err = tx.ExecContext(ctx, `DELETE FROM deployment_runs WHERE deployment_id = ? AND id NOT IN
		(SELECT id FROM deployment_runs WHERE deployment_id = ? ORDER BY id DESC LIMIT ?)`, r.Deployment, r.Deployment, RunsPerDeployment); err != nil {
		return 0, fmt.Errorf("bound deployment runs: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM deployment_runs WHERE id NOT IN
		(SELECT id FROM deployment_runs ORDER BY id DESC LIMIT ?)`, MaxRuns); err != nil {
		return 0, fmt.Errorf("bound deployment runs: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("add deployment run: %w", err)
	}
	return id, nil
}

// Wait waits up to maxWait for t's run to end and returns it; finished is false if it
// still goes on.
func (s *Service) Wait(ctx context.Context, d Deployment, t Ticket, maxWait time.Duration) (run Run, finished bool, err error) {
	select {
	case <-t.done:
		finished = true
	case <-s.clock.After(maxWait):
	case <-s.ctx.Done():
		// Shutting down: answer at once, so the server can stop.
	case <-ctx.Done():
		return Run{}, false, ctx.Err()
	}
	run, err = s.Run(ctx, d.ID, t.RunID)
	return run, finished, err
}

const runColumns = `id, deployment_id, actor_kind, actor_name, status, variables, previous, queued_at, started_at, finished_at, image_before, image_after, error`

func scanRun(row scanner) (Run, error) {
	var r Run
	var vars, prev string
	var queued int64
	var started, finished sql.NullInt64
	if err := row.Scan(&r.ID, &r.Deployment, &r.ActorKind, &r.ActorName, &r.Status, &vars, &prev, &queued, &started, &finished, &r.ImageBefore, &r.ImageAfter, &r.Error); err != nil {
		return Run{}, err
	}
	if err := json.Unmarshal([]byte(vars), &r.Variables); err != nil {
		return Run{}, fmt.Errorf("run %d variables: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(prev), &r.Previous); err != nil {
		return Run{}, fmt.Errorf("run %d previous variables: %w", r.ID, err)
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

// Run returns run id of deployment d (an ID; callers authorize first).
func (s *Service) Run(ctx context.Context, d, id int64) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM deployment_runs WHERE deployment_id = ? AND id = ?`, d, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("get deployment run: %w", err)
	}
	return r, nil
}

// Runs returns deployment d's runs, newest first.
func (s *Service) Runs(ctx context.Context, d int64) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM deployment_runs WHERE deployment_id = ? ORDER BY id DESC`, d)
	if err != nil {
		return nil, fmt.Errorf("list deployment runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list deployment runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deployment runs: %w", err)
	}
	return out, nil
}

// FailStale marks the runs a previous process left queued or running as failed; call
// it at start, before any deploy.
func (s *Service) FailStale(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE deployment_runs SET status = 'failed', finished_at = ?, error = ?
		WHERE status IN ('queued', 'running')`, s.clock.Now().UnixMilli(), "nudge restarted during the run"); err != nil {
		return fmt.Errorf("fail stale deployment runs: %w", err)
	}
	return nil
}

// Close stops the runs: the running deploys see their context end, queued ones fail
// without starting. It waits for them and refuses new deploys.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}
