// Package environments manages the Docker hosts nudge controls and their Docker
// clients. Each environment has a kind that decides how its client dials: "local" (the
// server's own Docker socket) now, "agent" (a remote host's tunnel) with phase 2.
package environments

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
)

// Kinds of environment.
const (
	KindLocal = "local"
	KindAgent = "agent"
)

// LocalName is the name of the environment EnsureLocal creates.
const LocalName = "local"

const (
	// pingTimeout bounds an online check, so one dead host can't stall a list.
	pingTimeout = 3 * time.Second
	// touchInterval throttles last_seen_at writes.
	touchInterval = time.Minute
)

// ErrNotFound means there is no environment with that id.
var ErrNotFound = errors.New("environment not found")

// Environment is a Docker host.
type Environment struct {
	ID        int64
	Name      string
	Kind      string
	CreatedAt time.Time
	// LastSeenAt is when the host last answered, nil if it never did.
	LastSeenAt *time.Time
	// Enrolled reports whether an agent environment has credentials; AgentVersion is the
	// nudge version its agent reported last.
	Enrolled     bool
	AgentVersion string
}

// ConnectFunc returns a Docker client for an environment.
type ConnectFunc func(Environment) (docker.Client, error)

// Options are the Service's dependencies.
type Options struct {
	DB  *sql.DB
	Log *slog.Logger
	// Now is the clock (time.Now by default).
	Now func() time.Time
	// Connect builds the client of an environment; it is called once per environment.
	Connect ConnectFunc
}

// Service lists environments and hands out their Docker clients.
type Service struct {
	db      *sql.DB
	log     *slog.Logger
	now     func() time.Time
	connect ConnectFunc

	mu sync.Mutex
	// clients are keyed by environment id, so bounded by the environments table.
	clients map[int64]docker.Client
}

// New returns a Service.
func New(opts Options) *Service {
	s := &Service{
		db:      opts.DB,
		log:     opts.Log,
		now:     opts.Now,
		connect: opts.Connect,
		clients: map[int64]docker.Client{},
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// LocalConnect connects local environments to the Docker host at host (unix:// or
// tcp://); other kinds are refused until they are implemented.
func LocalConnect(host string) (ConnectFunc, error) {
	dial, err := docker.LocalDialer(host)
	if err != nil {
		return nil, err
	}
	return func(env Environment) (docker.Client, error) {
		if env.Kind != KindLocal {
			return nil, fmt.Errorf("environment %d: kind %q is not supported yet", env.ID, env.Kind)
		}
		return docker.New(dial)
	}, nil
}

// EnsureLocal creates the local environment when its Docker host answers and no local
// environment exists yet. It reports whether it created one; an unreachable host is
// not an error, so nudge starts without Docker.
func (s *Service) EnsureLocal(ctx context.Context) (bool, error) {
	c, err := s.connect(Environment{Name: LocalName, Kind: KindLocal})
	if err != nil {
		return false, err
	}
	defer func() { _ = c.Close() }()
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	_, err = c.Ping(pingCtx, client.PingOptions{})
	cancel()
	if err != nil {
		s.log.Warn("local Docker host not reachable, no local environment", "err", err)
		return false, nil
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO environments (name, kind, created_at, last_seen_at)
		SELECT ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM environments WHERE kind = ?)
		ON CONFLICT (name) DO NOTHING`,
		LocalName, KindLocal, now, now, KindLocal)
	if err != nil {
		return false, fmt.Errorf("create local environment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("create local environment: %w", err)
	}
	return n > 0, nil
}

// List returns all environments, ordered by id.
func (s *Service) List(ctx context.Context) ([]Environment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM environments ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	envs := []Environment{}
	for rows.Next() {
		env, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list environments: %w", err)
		}
		envs = append(envs, env)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	return envs, nil
}

// Get returns the environment with id, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id int64) (Environment, error) {
	env, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM environments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, ErrNotFound
	}
	if err != nil {
		return Environment{}, fmt.Errorf("get environment %d: %w", id, err)
	}
	return env, nil
}

// Client returns the Docker client of env, creating it on first use.
func (s *Service) Client(env Environment) (docker.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[env.ID]; ok {
		return c, nil
	}
	c, err := s.connect(env)
	if err != nil {
		return nil, fmt.Errorf("connect environment %d: %w", env.ID, err)
	}
	s.clients[env.ID] = c
	return c, nil
}

// Online pings env's Docker host and records when it last answered. It reports false
// for any failure, which it logs at debug level.
func (s *Service) Online(ctx context.Context, env *Environment) bool {
	c, err := s.Client(*env)
	if err != nil {
		s.log.Debug("environment offline", "env", env.ID, "err", err)
		return false
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	_, err = c.Ping(pingCtx, client.PingOptions{})
	cancel()
	if err != nil {
		s.log.Debug("environment offline", "env", env.ID, "err", err)
		return false
	}
	s.Seen(ctx, env)
	return true
}

// Seen records that env's host answered just now, at most once per touchInterval.
func (s *Service) Seen(ctx context.Context, env *Environment) {
	now := s.now()
	if env.LastSeenAt != nil && now.Sub(*env.LastSeenAt) < touchInterval {
		return
	}
	// Best effort: a busy database must not fail a Docker request.
	if _, err := s.db.ExecContext(ctx, `UPDATE environments SET last_seen_at = ? WHERE id = ?`, now.Unix(), env.ID); err != nil {
		s.log.Warn("touch environment", "env", env.ID, "err", err)
		return
	}
	seen := time.Unix(now.Unix(), 0)
	env.LastSeenAt = &seen
}

// Close closes every Docker client.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for id, c := range s.clients {
		if err := c.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close environment %d: %w", id, err))
		}
		delete(s.clients, id)
	}
	return errors.Join(errs...)
}

// columns are what scan reads.
const columns = `id, name, kind, created_at, last_seen_at, agent_id IS NOT NULL, COALESCE(agent_version, '')`

type scanner interface{ Scan(...any) error }

func scan(r scanner) (Environment, error) {
	var env Environment
	var created int64
	var lastSeen sql.NullInt64
	if err := r.Scan(&env.ID, &env.Name, &env.Kind, &created, &lastSeen, &env.Enrolled, &env.AgentVersion); err != nil {
		return Environment{}, err
	}
	env.CreatedAt = time.Unix(created, 0)
	if lastSeen.Valid {
		ls := time.Unix(lastSeen.Int64, 0)
		env.LastSeenAt = &ls
	}
	return env, nil
}
