package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
)

const (
	// ActivityLook is how often Activity looks for environments to read events from.
	ActivityLook = 30 * time.Second
	// MaxActivityDays bounds the days ActivityDays answers.
	MaxActivityDays = 30
	// activityName is the activity counter's reserved subscription.
	activityName = "activity"
	// activityPings bounds the environments pinged at once.
	activityPings = 8
	// maxPendingActivity bounds the unflushed buckets; past it new owners' counts go to
	// the environment's unowned bucket, so the total stays right.
	maxPendingActivity = 10_000
	// maxActivityOwners bounds a reader's cache of container owners; when full it
	// starts over.
	maxActivityOwners = 4096
	// ownerTimeout bounds one owner lookup.
	ownerTimeout = 10 * time.Second
)

// ActivityEnvs is what Activity needs of the environments.
type ActivityEnvs interface {
	Envs
	Online(ctx context.Context, env *environments.Environment) bool
}

// EventOwnerFunc returns the owner key of the container id, whose event carried attrs
// (its labels), or "" if no user owns it: the API's ownership rule.
type EventOwnerFunc func(ctx context.Context, c docker.Client, id string, attrs map[string]string) (string, error)

// ActivityOptions are Activity's dependencies.
type ActivityOptions struct {
	DB    *sql.DB
	Log   *slog.Logger
	Envs  ActivityEnvs
	Hub   *events.Hub
	Owner EventOwnerFunc
	Clock Clock
}

// Activity counts each environment's container events per hour, by the container's
// owner, for the activity heatmap. A reserved events.Hub subscription per online
// environment feeds it; the counts wait in memory and go to metrics.db every hour and
// when Run ends, and reads add the ones still waiting. Exec events are not counted:
// health checks fire them every interval and would drown the rest. Events while an
// environment's stream is down (until the next look) or the server is down are missed.
type Activity struct {
	db    *sql.DB
	log   *slog.Logger
	envs  ActivityEnvs
	hub   *events.Hub
	owner EventOwnerFunc
	clock Clock

	// flushMu keeps reads out while a flush moves counts from pending to the database,
	// so they are counted once.
	flushMu sync.RWMutex
	mu      sync.Mutex
	// pending are the unflushed counts, bounded by maxPendingActivity.
	pending map[activityKey]int64

	// The fields below are owned by Run's goroutine.
	wg sync.WaitGroup
	// readers are the environments' event readers by environment id (bounded by the
	// environments table).
	readers map[int64]activityReader
}

type activityKey struct {
	env   int64
	hour  int64
	owner string
}

type activityReader struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewActivity returns an Activity; Run starts counting.
func NewActivity(opts ActivityOptions) *Activity {
	a := &Activity{
		db: opts.DB, log: opts.Log, envs: opts.Envs, hub: opts.Hub, owner: opts.Owner, clock: opts.Clock,
		pending: map[activityKey]int64{}, readers: map[int64]activityReader{},
	}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	if a.clock == nil {
		a.clock = realClock{}
	}
	return a
}

// Run counts until ctx ends: every ActivityLook it starts a reader for each online
// environment without one and stops those of removed ones, and once an hour it
// flushes. When ctx ends it stops the readers and flushes what is left.
func (a *Activity) Run(ctx context.Context) {
	defer func() {
		for _, r := range a.readers {
			r.cancel()
		}
		a.wg.Wait()
		if err := a.Flush(context.WithoutCancel(ctx)); err != nil {
			a.log.Warn("activity flush", "err", err)
		}
	}()
	flushed := a.clock.Now().Truncate(time.Hour)
	for {
		a.look(ctx)
		if hour := a.clock.Now().Truncate(time.Hour); hour.After(flushed) {
			if err := a.Flush(ctx); err != nil && ctx.Err() == nil {
				a.log.Warn("activity flush", "err", err)
			}
			flushed = hour
		}
		select {
		case <-ctx.Done():
			return
		case <-a.clock.After(ActivityLook):
		}
	}
}

// look starts a reader for each online environment that has none (only those are
// pinged) and stops the readers of removed environments.
func (a *Activity) look(ctx context.Context) {
	list, err := a.envs.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			a.log.Warn("activity: list environments", "err", err)
		}
		return
	}
	seen := map[int64]bool{}
	var idle []environments.Environment
	for _, env := range list {
		seen[env.ID] = true
		if r, ok := a.readers[env.ID]; ok {
			select {
			case <-r.done:
				delete(a.readers, env.ID)
			default:
				continue
			}
		}
		idle = append(idle, env)
	}
	for id, r := range a.readers {
		if !seen[id] {
			r.cancel()
			delete(a.readers, id)
		}
	}
	online := make([]bool, len(idle))
	var wg sync.WaitGroup
	slots := make(chan struct{}, activityPings)
	for i := range idle {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			online[i] = a.envs.Online(ctx, &idle[i])
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	for i, env := range idle {
		if online[i] {
			a.startReader(ctx, env)
		}
	}
}

func (a *Activity) startReader(ctx context.Context, env environments.Environment) {
	c, err := a.envs.Client(env)
	if err != nil {
		a.log.Warn("activity: environment client", "env", env.ID, "err", err)
		return
	}
	sub, err := a.hub.SubscribeReserved(env.ID, activityName, c)
	if err != nil {
		a.log.Warn("activity: watch events", "env", env.ID, "err", err)
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	r := activityReader{cancel: cancel, done: make(chan struct{})}
	a.readers[env.ID] = r
	a.wg.Go(func() {
		defer close(r.done)
		defer sub.Close()
		a.read(rctx, env.ID, c, sub)
	})
}

// read counts env's container events until its stream ends or ctx is done.
func (a *Activity) read(ctx context.Context, env int64, c docker.Client, sub *events.Subscription) {
	// owners caches each container's owner key: its labels and image never change, and
	// a destroyed one can't be looked up any more.
	owners := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.C():
			if !ok {
				return
			}
			if e.Type != "container" || strings.HasPrefix(e.Action, "exec_") {
				continue
			}
			a.record(env, a.ownerOf(ctx, c, owners, e), a.clock.Now())
		}
	}
}

// ownerOf returns the owner key of e's container; a failed lookup counts it as
// nobody's and is tried again at its next event.
func (a *Activity) ownerOf(ctx context.Context, c docker.Client, owners map[string]string, e events.Event) string {
	if a.owner == nil {
		return ""
	}
	if key, ok := owners[e.ID]; ok {
		return key
	}
	octx, cancel := context.WithTimeout(ctx, ownerTimeout)
	key, err := a.owner(octx, c, e.ID, e.Attributes)
	cancel()
	if err != nil {
		a.log.Debug("activity: owner", "container", e.ID, "err", err)
		return ""
	}
	if len(owners) >= maxActivityOwners {
		clear(owners)
	}
	owners[e.ID] = key
	return key
}

// record counts one event of env at t under owner.
func (a *Activity) record(env int64, owner string, t time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.add(activityKey{env: env, hour: t.Truncate(time.Hour).Unix(), owner: owner}, 1)
}

// add adds n to k's pending count, under the unowned bucket once pending is full. The
// caller holds a.mu.
func (a *Activity) add(k activityKey, n int64) {
	if _, ok := a.pending[k]; !ok && len(a.pending) >= maxPendingActivity {
		k.owner = ""
	}
	a.pending[k] += n
}

// Flush adds the waiting counts to the database in one transaction; if it fails they
// wait for the next flush.
func (a *Activity) Flush(ctx context.Context) error {
	a.flushMu.Lock()
	defer a.flushMu.Unlock()
	a.mu.Lock()
	batch := a.pending
	a.pending = map[activityKey]int64{}
	a.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	if err := a.write(ctx, batch); err != nil {
		a.mu.Lock()
		for k, n := range batch {
			a.add(k, n)
		}
		a.mu.Unlock()
		return fmt.Errorf("flush activity: %w", err)
	}
	return nil
}

func (a *Activity) write(ctx context.Context, batch map[activityKey]int64) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for k, n := range batch {
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity_hours (environment_id, hour, owner, count)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (environment_id, hour, owner) DO UPDATE SET count = count + excluded.count`,
			k.env, k.hour, k.owner, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ActivityDay is one day's container events, by hour of the day.
type ActivityDay struct {
	// Date is the day in the requested time zone, as 2006-01-02.
	Date   string  `json:"date"`
	Counts []int64 `json:"counts"`
}

// ErrActivityDays refuses a number of days outside 1 to MaxActivityDays.
var ErrActivityDays = fmt.Errorf("days: want 1 to %d", MaxActivityDays)

// Days answers env's container events per hour (in sc's scope: every owner's, or one
// owner's) for the last days days in loc, today last, 24 hours each. An hour is filed
// under the local hour it starts in: a day the clocks go forward has an empty hour, one
// they go back has two hours in one.
func (a *Activity) Days(ctx context.Context, env int64, sc Scope, days int, loc *time.Location) ([]ActivityDay, error) {
	if days < 1 || days > MaxActivityDays {
		return nil, ErrActivityDays
	}
	now := a.clock.Now().In(loc)
	y, m, d := now.Date()
	out := make([]ActivityDay, days)
	index := make(map[string]int, days)
	for i := range out {
		day := time.Date(y, m, d-(days-1-i), 0, 0, 0, 0, loc)
		out[i] = ActivityDay{Date: day.Format(time.DateOnly), Counts: make([]int64, 24)}
		index[out[i].Date] = i
	}
	if !sc.All && sc.Owner == "" {
		return out, nil
	}
	from := time.Date(y, m, d-(days-1), 0, 0, 0, 0, loc).Unix()
	to := time.Date(y, m, d+1, 0, 0, 0, 0, loc).Unix()
	add := func(hour, n int64) {
		t := time.Unix(hour, 0).In(loc)
		if i, ok := index[t.Format(time.DateOnly)]; ok {
			out[i].Counts[t.Hour()] += n
		}
	}
	a.flushMu.RLock()
	defer a.flushMu.RUnlock()
	if err := a.stored(ctx, env, sc, from, to, add); err != nil {
		return nil, fmt.Errorf("activity of environment %d: %w", env, err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, n := range a.pending {
		if k.env == env && k.hour >= from && k.hour < to && (sc.All || k.owner == sc.Owner) {
			add(k.hour, n)
		}
	}
	return out, nil
}

// stored calls add with env's stored counts per hour in [from, to).
func (a *Activity) stored(ctx context.Context, env int64, sc Scope, from, to int64, add func(hour, n int64)) error {
	query := `SELECT hour, SUM(count) FROM activity_hours
		WHERE environment_id = ? AND hour >= ? AND hour < ? AND (? OR owner = ?) GROUP BY hour`
	rows, err := a.db.QueryContext(ctx, query, env, from, to, sc.All, sc.Owner)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var hour, n int64
		if err := rows.Scan(&hour, &n); err != nil {
			return err
		}
		add(hour, n)
	}
	return rows.Err()
}
