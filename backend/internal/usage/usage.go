// Package usage measures disk usage in the background and keeps the last measurement:
// for now the size of every volume. Docker computes it by walking each volume, which is
// too slow for a list request, so the sizes are measured on a schedule (or on demand)
// and stored in volume_usage.
package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

const (
	// measureTimeout bounds one environment's measurement; a host with large volumes
	// takes a while to walk.
	measureTimeout = 5 * time.Minute
	// maxMeasures is how many environments are measured at once.
	maxMeasures = 2
)

var (
	// ErrRunning means the environment is being measured already.
	ErrRunning = errors.New("a measurement of this environment is running")
	// ErrSave wraps a failure to store a measurement (not the Docker host's fault).
	ErrSave = errors.New("save volume usage")
)

// Envs is what Service needs of the environments.
type Envs interface {
	List(ctx context.Context) ([]environments.Environment, error)
	Client(env environments.Environment) (docker.Client, error)
}

// Clock is the time source; tests skip the waits.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options are the Service's dependencies.
type Options struct {
	DB    *sql.DB
	Log   *slog.Logger
	Envs  Envs
	Clock Clock
}

// Service measures environments' disk usage and keeps the last results.
type Service struct {
	db    *sql.DB
	log   *slog.Logger
	envs  Envs
	clock Clock

	mu sync.Mutex
	// busy holds the environments being measured, so bounded by the environments table.
	busy map[int64]bool
}

// New returns a Service.
func New(opts Options) *Service {
	s := &Service{db: opts.DB, log: opts.Log, envs: opts.Envs, clock: opts.Clock, busy: map[int64]bool{}}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.clock == nil {
		s.clock = realClock{}
	}
	return s
}

// Volume is a volume's last measured usage.
type Volume struct {
	// Size is in bytes, nil when the volume's driver can't report it.
	Size *int64
	// RefCount is how many containers use the volume, -1 if unknown.
	RefCount   int64
	MeasuredAt time.Time
}

// Volumes returns the last measurement of env's volumes by name; volumes created since
// are missing.
func (s *Service) Volumes(ctx context.Context, env int64) (map[string]Volume, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, size, ref_count, measured_at FROM volume_usage WHERE environment_id = ?`, env)
	if err != nil {
		return nil, fmt.Errorf("volume usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	vols := map[string]Volume{}
	for rows.Next() {
		var (
			name string
			size sql.NullInt64
			v    Volume
			at   int64
		)
		if err := rows.Scan(&name, &size, &v.RefCount, &at); err != nil {
			return nil, fmt.Errorf("volume usage: %w", err)
		}
		if size.Valid {
			v.Size = &size.Int64
		}
		v.MeasuredAt = time.UnixMilli(at)
		vols[name] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("volume usage: %w", err)
	}
	return vols, nil
}

// Measure measures env's volumes and replaces its last measurement; ErrRunning if a
// measurement of env is running.
func (s *Service) Measure(ctx context.Context, env environments.Environment) error {
	if !s.lock(env.ID) {
		return ErrRunning
	}
	defer s.unlock(env.ID)
	c, err := s.envs.Client(env)
	if err != nil {
		return fmt.Errorf("measure environment %d: %w", env.ID, err)
	}
	// The timeout is the walk's alone, so a slow one still leaves time to save.
	dctx, cancel := context.WithTimeout(ctx, measureTimeout)
	res, err := c.DiskUsage(dctx, client.DiskUsageOptions{Volumes: true, Verbose: true})
	cancel()
	if err != nil {
		return fmt.Errorf("measure environment %d: %w", env.ID, err)
	}
	if err := s.save(ctx, env.ID, res.Volumes, s.clock.Now()); err != nil {
		return fmt.Errorf("%w: %w", ErrSave, err)
	}
	return nil
}

func (s *Service) save(ctx context.Context, env int64, du client.VolumesDiskUsage, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Volumes removed since the last measurement go with the old rows.
	if _, err := tx.ExecContext(ctx, `DELETE FROM volume_usage WHERE environment_id = ?`, env); err != nil {
		return err
	}
	for _, v := range du.Items {
		var size sql.NullInt64
		refs := int64(-1)
		if u := v.UsageData; u != nil {
			size = sql.NullInt64{Int64: u.Size, Valid: u.Size >= 0}
			refs = u.RefCount
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO volume_usage (environment_id, name, size, ref_count, measured_at) VALUES (?, ?, ?, ?, ?)`,
			env, v.Name, size, refs, now.UnixMilli()); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// MeasureAll measures every environment, maxMeasures at once. Offline environments and
// ones being measured already are skipped.
func (s *Service) MeasureAll(ctx context.Context) error {
	envs, err := s.envs.List(ctx)
	if err != nil {
		return fmt.Errorf("measure usage: %w", err)
	}
	slots := make(chan struct{}, maxMeasures)
	var wg sync.WaitGroup
	for _, env := range envs {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-slots }()
			err := s.Measure(ctx, env)
			switch {
			case err == nil, ctx.Err() != nil, errors.Is(err, ErrRunning):
			case docker.Unreachable(err):
				s.log.Debug("usage measurement skipped: environment offline", "env", env.ID)
			default:
				s.log.Warn("usage measurement", "env", env.ID, "err", err)
			}
		})
	}
	wg.Wait()
	return ctx.Err()
}

// Run measures every environment after delay and then every interval until ctx ends.
func (s *Service) Run(ctx context.Context, delay, interval time.Duration) {
	wait := delay
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(wait):
		}
		if err := s.MeasureAll(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("usage measurement", "err", err)
		}
		wait = interval
	}
}

func (s *Service) lock(env int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[env] {
		return false
	}
	s.busy[env] = true
	return true
}

func (s *Service) unlock(env int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, env)
}
