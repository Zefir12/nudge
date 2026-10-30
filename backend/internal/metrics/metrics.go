// Package metrics collects the CPU, memory, network and block I/O usage of every
// environment and container in the background and keeps it in its own database,
// <data>/metrics.db: raw samples for RawRetention, hourly rollups (average and maximum)
// for the configured retention. It is not part of backups: it is rewritten every tick
// and worth little next to the rest.
//
// Run keeps one collector goroutine per environment, started and stopped as
// environments come and go; each samples its environment every interval and stores the
// tick in one transaction. While an environment has a live subscriber (SubscribeLive)
// it is sampled every live interval instead; those extra ticks only go to the
// subscribers, so the stored samples stay at the background interval. A cleanup on a timer rolls finished hours up and drops what
// is past retention and the rows of removed environments.
package metrics

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/hostload"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// FileName is the metrics database inside the data directory.
const FileName = "metrics.db"

const (
	// RawRetention is how long raw samples are kept; older ones live on as rollups.
	RawRetention = 48 * time.Hour
	// DefaultInterval is the configuration's default collection interval.
	DefaultInterval = time.Minute
	// DefaultRetention is the configuration's default retention of hourly rollups.
	DefaultRetention = 30 * 24 * time.Hour
	// collectTimeout bounds one tick's sampling: at MaxConcurrentStats a second, enough
	// for a few hundred containers.
	collectTimeout = time.Minute
	// cleanupEvery is how often hours are rolled up and old rows dropped.
	cleanupEvery = 10 * time.Minute
	// staleTicks is how many intervals old the latest sample may be and still count as
	// the environment's current usage.
	staleTicks = 3
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if needed) <dataDir>/metrics.db with its migrations.
func Open(ctx context.Context, dataDir string) (*sql.DB, error) {
	return store.OpenDB(ctx, filepath.Join(dataDir, FileName), migrationsFS, "migrations")
}

// Envs is what Service needs of the environments.
type Envs interface {
	List(ctx context.Context) ([]environments.Environment, error)
	Client(env environments.Environment) (docker.Client, error)
}

// OwnersFunc returns the owner key of each of items a user owns, by container ID; the
// API's ownership rule (the owner label, unless the image sets one too).
type OwnersFunc func(ctx context.Context, c docker.Client, items []container.Summary) (map[string]string, error)

// HostFunc reads the host of an environment (hostload.Read locally, hostload.Remote
// for an agent).
type HostFunc func(ctx context.Context, env environments.Environment) (hostload.Reading, error)

// Host is the whole machine's load at a tick: CPU as a percentage of every CPU (0 to
// 100, not of one CPU like the containers'), memory in use (all but what is available)
// and all of it.
type Host struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryUsage uint64  `json:"memory_usage"`
	MemoryTotal uint64  `json:"memory_total"`
}

// Clock is the time source; tests drive the waits.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options are the Service's dependencies.
type Options struct {
	DB     *sql.DB
	Log    *slog.Logger
	Envs   Envs
	Owners OwnersFunc
	// Host reads an environment's host each tick; nil leaves the host's load out.
	Host  HostFunc
	Clock Clock
	// Interval is how often each environment is sampled; zero means collection is off
	// (Run returns at once), and stored samples still answer.
	Interval time.Duration
	// Retention is how long hourly rollups are kept, DefaultRetention if zero.
	Retention time.Duration
	// LiveInterval is how often a watched environment is sampled, DefaultLiveInterval
	// if zero.
	LiveInterval time.Duration
}

// Service collects samples and answers them.
type Service struct {
	db        *sql.DB
	log       *slog.Logger
	envs      Envs
	owners    OwnersFunc
	host      HostFunc
	clock     Clock
	interval  time.Duration
	retention time.Duration
	// liveInterval is how often a watched environment is sampled.
	liveInterval time.Duration
	live         *liveHub
}

// New returns a Service.
func New(opts Options) *Service {
	s := &Service{
		db: opts.DB, log: opts.Log, envs: opts.Envs, owners: opts.Owners, host: opts.Host, clock: opts.Clock,
		interval: opts.Interval, retention: opts.Retention, liveInterval: opts.LiveInterval,
		live: newLiveHub(),
	}
	if s.liveInterval <= 0 {
		s.liveInterval = DefaultLiveInterval
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.clock == nil {
		s.clock = realClock{}
	}
	if s.retention <= 0 {
		s.retention = DefaultRetention
	}
	return s
}

// Interval is the configured collection interval, zero when collection is off.
func (s *Service) Interval() time.Duration { return s.interval }

// Run collects until ctx ends: every interval it starts a collector for each new
// environment, stops those of removed ones, and every cleanupEvery runs Cleanup. It
// returns at once when collection is off, and waits for its collectors; live
// subscriptions end with it.
func (s *Service) Run(ctx context.Context) {
	if s.interval <= 0 {
		return
	}
	workers := map[int64]context.CancelFunc{}
	var wg sync.WaitGroup
	defer func() {
		for _, cancel := range workers {
			cancel()
		}
		wg.Wait()
		s.live.close()
	}()
	var nextCleanup time.Time
	for {
		envs, err := s.envs.List(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("metrics: list environments", "err", err)
		}
		if err == nil {
			ids := make([]int64, 0, len(envs))
			seen := make(map[int64]bool, len(envs))
			for _, env := range envs {
				ids = append(ids, env.ID)
				seen[env.ID] = true
				if workers[env.ID] == nil {
					wctx, cancel := context.WithCancel(ctx)
					workers[env.ID] = cancel
					wg.Go(func() { s.collectLoop(wctx, env) })
				}
			}
			for id, cancel := range workers {
				if !seen[id] {
					cancel()
					delete(workers, id)
				}
			}
			if now := s.clock.Now(); !now.Before(nextCleanup) {
				if err := s.Cleanup(ctx, now, ids); err != nil && ctx.Err() == nil {
					s.log.Warn("metrics cleanup", "err", err)
				}
				nextCleanup = now.Add(cleanupEvery)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(s.interval):
		}
	}
}

// collectLoop samples env until ctx ends: now and then every interval, the ticks it
// stores, and while env is watched also every live interval, ticks that only go to
// the live subscribers. The first subscriber wakes it, so watching starts at once.
func (s *Service) collectLoop(ctx context.Context, env environments.Environment) {
	wake, unregister := s.live.register(env.ID)
	defer unregister()
	// nextSave is when the next stored tick is due; last is when the last tick started.
	var nextSave, last time.Time
	// The host's CPU is the change since a previous reading.
	var prev hostReadings
	for {
		now := s.clock.Now()
		save := !now.Before(nextSave)
		watched := s.live.watched(env.ID)
		if save || watched && !now.Before(last.Add(s.liveInterval)) {
			s.logCollect(ctx, env, s.collect(ctx, env, save, &prev))
			last = now
			if save {
				nextSave = now.Add(s.interval)
			}
		}
		wait := nextSave.Sub(now)
		if s.live.watched(env.ID) {
			wait = min(wait, last.Add(s.liveInterval).Sub(now))
		}
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(max(wait, 0)):
		case <-wake:
		}
	}
}

func (s *Service) logCollect(ctx context.Context, env environments.Environment, err error) {
	switch {
	case err == nil, ctx.Err() != nil:
	case docker.Unreachable(err):
		s.log.Debug("metrics skipped: environment offline", "env", env.ID)
	default:
		s.log.Warn("metrics", "env", env.ID, "err", err)
	}
}

// Collect takes one sample of every running container of env and stores it as one
// tick. A tick cut short, or one where no listed container could be sampled, is not
// stored: its totals would look whole.
//
// Without a previous reading of the host, the tick leaves the host's load out.
func (s *Service) Collect(ctx context.Context, env environments.Environment) error {
	return s.collect(ctx, env, true, &hostReadings{})
}

// hostReadTimeout bounds a tick's read of its host, apart from the containers' budget:
// a slow agent leaves the host out rather than the tick.
const hostReadTimeout = 5 * time.Second

// hostReadings are a collector's previous readings of its host: of the last tick, which
// a live tick's CPU is measured from, and of the last stored tick, which a stored tick's
// is, so the stored CPU covers the whole interval whether or not anyone watches.
type hostReadings struct {
	last, saved hostload.Reading
	// lastOK and savedOK tell whether last and saved hold a reading.
	lastOK, savedOK bool
	// failing is set while reads fail, so a failure is logged once, not every tick.
	failing bool
}

// readHost reads env's host and returns its load since the last tick (live) and, when
// save is set, since the last stored tick (stored); nil when it can't be told (no
// reader, an older agent, no previous reading). A failed read starts over.
func (s *Service) readHost(ctx context.Context, env environments.Environment, prev *hostReadings, save bool) (live, stored *Host) {
	if s.host == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, hostReadTimeout)
	defer cancel()
	r, err := s.host(ctx, env)
	if err != nil {
		if !errors.Is(err, hostload.ErrUnknown) && !prev.failing {
			s.log.Warn("metrics: host load", "env", env.ID, "err", err)
		}
		*prev = hostReadings{failing: true}
		return nil, nil
	}
	since := func(from hostload.Reading, ok bool) *Host {
		if !ok {
			return nil
		}
		cpu, ok := hostload.CPUPercent(from, r)
		if !ok {
			return nil
		}
		return &Host{CPUPercent: cpu, MemoryUsage: r.MemoryUsed(), MemoryTotal: r.MemoryTotal}
	}
	live = since(prev.last, prev.lastOK)
	prev.last, prev.lastOK, prev.failing = r, true, false
	if save {
		stored = since(prev.saved, prev.savedOK)
		prev.saved, prev.savedOK = r, true
	}
	return live, stored
}

// collect takes one tick of env, hands it to env's live subscribers, and stores it if
// save is set; prev are the previous readings of env's host.
func (s *Service) collect(ctx context.Context, env environments.Environment, save bool, prev *hostReadings) error {
	c, err := s.envs.Client(env)
	if err != nil {
		return fmt.Errorf("collect environment %d: %w", env.ID, err)
	}
	now := s.clock.Now()
	liveHost, storedHost := s.readHost(ctx, env, prev, save)
	tctx, cancel := context.WithTimeout(ctx, collectTimeout)
	defer cancel()
	list, err := c.ContainerList(tctx, client.ContainerListOptions{})
	if err != nil {
		return fmt.Errorf("collect environment %d: %w", env.ID, err)
	}
	owners := map[string]string{}
	if s.owners != nil {
		if owners, err = s.owners(tctx, c, list.Items); err != nil {
			return fmt.Errorf("collect environment %d: owners: %w", env.ID, err)
		}
	}
	sampled, skipped := SampleContainers(tctx, c, list.Items, s.log)
	if err := tctx.Err(); err != nil {
		return fmt.Errorf("collect environment %d: %w", env.ID, err)
	}
	if len(sampled) == 0 && skipped > 0 {
		return fmt.Errorf("collect environment %d: %w", env.ID, errNoneSampled)
	}
	s.live.publish(env.ID, newTick(now, list.Items, sampled, owners, liveHost))
	if !save {
		return nil
	}
	if err := s.save(ctx, env.ID, now, sampled, owners, storedHost); err != nil {
		return fmt.Errorf("save metrics of environment %d: %w", env.ID, err)
	}
	return nil
}

var errNoneSampled = errors.New("no container could be sampled")

func (s *Service) save(ctx context.Context, env int64, now time.Time, sampled []Container, owners map[string]string, host *Host) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	t := now.Unix()
	var total Sample
	for _, c := range sampled {
		total.CPUPercent += c.CPUPercent
		total.MemoryUsage += c.MemoryUsage
		total.NetworkRx += c.NetworkRx
		total.NetworkTx += c.NetworkTx
		total.BlockRead += c.BlockRead
		total.BlockWrite += c.BlockWrite
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO container_samples
			(environment_id, container_id, time, name, owner, cpu, memory, network_rx, network_tx, block_read, block_write)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			env, c.ID, t, c.Name, owners[c.ID], c.CPUPercent, i64(c.MemoryUsage),
			i64(c.NetworkRx), i64(c.NetworkTx), i64(c.BlockRead), i64(c.BlockWrite)); err != nil {
			return err
		}
	}
	var hostCPU, hostMem, hostTotal any
	if host != nil {
		hostCPU, hostMem, hostTotal = host.CPUPercent, i64(host.MemoryUsage), i64(host.MemoryTotal)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO environment_samples
		(environment_id, time, cpu, memory, network_rx, network_tx, block_read, block_write, containers,
			host_cpu, host_memory, host_memory_total)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		env, t, total.CPUPercent, i64(total.MemoryUsage), i64(total.NetworkRx), i64(total.NetworkTx),
		i64(total.BlockRead), i64(total.BlockWrite), len(sampled), hostCPU, hostMem, hostTotal); err != nil {
		return err
	}
	return tx.Commit()
}

// i64 stores a counter in SQLite's signed INTEGER; no real counter reaches the cap.
func i64(v uint64) int64 {
	if v > 1<<63-1 {
		return 1<<63 - 1
	}
	return int64(v)
}
