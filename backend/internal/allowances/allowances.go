// Package allowances keeps what each user's containers may take in total per environment
// (CPU, memory, disk, containers, host ports, images) and decides whether a container fits:
// Check is the one rule, which the API runs under the user's Lock before every create,
// recreate, update and start of a user's container.
package allowances

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	// MaxImages bounds an allowance's image patterns.
	MaxImages = 50
	// maxPattern bounds one image pattern.
	maxPattern = 255
)

// ErrNotFound means the user has no allowance in the environment.
var ErrNotFound = errors.New("no allowance")

// Allowance is what a user's containers may take in total in one environment.
type Allowance struct {
	CPUsMilli     int64
	MemoryBytes   int64
	DiskBytes     int64
	MaxContainers int64
	// PortMin..PortMax are the host ports the user may publish; both 0 allows none.
	PortMin, PortMax int
	// Images are patterns of the images the user may run (path.Match on the reference,
	// or on its repository without the tag or digest); empty allows any.
	Images    []string
	UpdatedAt time.Time
}

// Validate returns what is wrong with a, by field ("port_max"), or nil.
func (a Allowance) Validate() map[string]string {
	errs := map[string]string{}
	for field, v := range map[string]int64{
		"cpus_milli": a.CPUsMilli, "memory_bytes": a.MemoryBytes, "disk_bytes": a.DiskBytes, "max_containers": a.MaxContainers,
	} {
		if v < 0 {
			errs[field] = "must not be negative"
		}
	}
	switch {
	case a.PortMin < 0 || a.PortMin > 65535:
		errs["port_min"] = "must be a port (1 to 65535), or 0 with port_max 0 for none"
	case a.PortMax < a.PortMin || a.PortMax > 65535:
		errs["port_max"] = "must be a port from port_min to 65535"
	case a.PortMin == 0 && a.PortMax != 0:
		errs["port_min"] = "must be a port (1 to 65535) when port_max is set"
	}
	if len(a.Images) > MaxImages {
		errs["images"] = fmt.Sprintf("at most %d patterns", MaxImages)
	}
	for i, p := range a.Images {
		if _, err := path.Match(p, ""); err != nil || p == "" || len(p) > maxPattern || strings.ContainsAny(p, " \t\n") {
			errs[fmt.Sprintf("images.%d", i)] = "must be an image pattern such as nginx or ghcr.io/me/*"
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Ports reports whether a lets the user publish host ports at all.
func (a Allowance) Ports() bool { return a.PortMin > 0 }

// ImageAllowed reports whether a lets the user run image ref.
func (a Allowance) ImageAllowed(ref string) bool {
	if len(a.Images) == 0 {
		return true
	}
	repo := repository(ref)
	for _, p := range a.Images {
		if ok, _ := path.Match(p, ref); ok {
			return true
		}
		if ok, _ := path.Match(p, repo); ok {
			return true
		}
	}
	return false
}

// repository is ref without its tag or digest.
func repository(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}

// Options are the Service's dependencies.
type Options struct {
	DB *sql.DB
	// Now is time.Now outside tests.
	Now func() time.Time
}

// Service stores allowances and image pulls and serializes each user's checks.
type Service struct {
	db  *sql.DB
	now func() time.Time

	mu sync.Mutex
	// locks holds a lock per user and environment while anyone holds or waits for it, so
	// it stays as small as the checks running.
	locks map[lockKey]*userLock
}

type lockKey struct{ user, env int64 }

type userLock struct {
	ch   chan struct{}
	refs int
}

// New returns a Service.
func New(opts Options) *Service {
	s := &Service{db: opts.DB, now: opts.Now, locks: map[lockKey]*userLock{}}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Lock takes user's lock in env, so their checks and the changes they allow run one at a
// time: two creates can't both fit in what is left for one. It waits until the lock is
// free or ctx ends.
func (s *Service) Lock(ctx context.Context, user, env int64) (unlock func(), err error) {
	key := lockKey{user, env}
	s.mu.Lock()
	l := s.locks[key]
	if l == nil {
		l = &userLock{ch: make(chan struct{}, 1)}
		s.locks[key] = l
	}
	l.refs++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if l.refs--; l.refs == 0 {
			delete(s.locks, key)
		}
	}
	select {
	case l.ch <- struct{}{}:
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-l.ch
			release()
		})
	}, nil
}

// Get returns user's allowance in env, ErrNotFound if they have none.
func (s *Service) Get(ctx context.Context, user, env int64) (Allowance, error) {
	var (
		a      Allowance
		images string
		at     int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT cpus_milli, memory_bytes, disk_bytes, max_containers, port_min, port_max, images, updated_at
		FROM allowances WHERE user_id = ? AND environment_id = ?`, user, env).
		Scan(&a.CPUsMilli, &a.MemoryBytes, &a.DiskBytes, &a.MaxContainers, &a.PortMin, &a.PortMax, &images, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Allowance{}, ErrNotFound
	}
	if err != nil {
		return Allowance{}, fmt.Errorf("get allowance: %w", err)
	}
	if err := json.Unmarshal([]byte(images), &a.Images); err != nil {
		return Allowance{}, fmt.Errorf("get allowance: images: %w", err)
	}
	a.UpdatedAt = time.UnixMilli(at)
	return a, nil
}

// Set replaces user's allowance in env with a, which must be valid; the user and the
// environment must exist.
func (s *Service) Set(ctx context.Context, user, env int64, a Allowance) (Allowance, error) {
	if errs := a.Validate(); errs != nil {
		return Allowance{}, fmt.Errorf("set allowance: invalid %v", errs)
	}
	if a.Images == nil {
		a.Images = []string{}
	}
	images, err := json.Marshal(a.Images)
	if err != nil {
		return Allowance{}, fmt.Errorf("set allowance: %w", err)
	}
	a.UpdatedAt = time.UnixMilli(s.now().UnixMilli())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO allowances (user_id, environment_id, cpus_milli, memory_bytes, disk_bytes, max_containers, port_min, port_max, images, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, environment_id) DO UPDATE SET
			cpus_milli = excluded.cpus_milli, memory_bytes = excluded.memory_bytes, disk_bytes = excluded.disk_bytes,
			max_containers = excluded.max_containers, port_min = excluded.port_min, port_max = excluded.port_max,
			images = excluded.images, updated_at = excluded.updated_at`,
		user, env, a.CPUsMilli, a.MemoryBytes, a.DiskBytes, a.MaxContainers, a.PortMin, a.PortMax, string(images), a.UpdatedAt.UnixMilli())
	if err != nil {
		return Allowance{}, fmt.Errorf("set allowance: %w", err)
	}
	return a, nil
}

// Delete removes user's allowance in env; ErrNotFound if there was none.
func (s *Service) Delete(ctx context.Context, user, env int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM allowances WHERE user_id = ? AND environment_id = ?`, user, env)
	if err != nil {
		return fmt.Errorf("delete allowance: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("delete allowance: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordPull notes that user's pull brought image (an ID) onto env.
func (s *Service) RecordPull(ctx context.Context, user, env int64, image string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO image_pulls (user_id, environment_id, image_id, pulled_at) VALUES (?, ?, ?, ?)`,
		user, env, image, s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("record pull: %w", err)
	}
	return nil
}

// ForgetPull drops image from user's pulls in env (it is gone, or was removed again).
func (s *Service) ForgetPull(ctx context.Context, user, env int64, image string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM image_pulls WHERE user_id = ? AND environment_id = ? AND image_id = ?`, user, env, image)
	if err != nil {
		return fmt.Errorf("forget pull: %w", err)
	}
	return nil
}

// Pulls returns the images user's pulls brought onto env, by ID.
func (s *Service) Pulls(ctx context.Context, user, env int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT image_id FROM image_pulls WHERE user_id = ? AND environment_id = ? ORDER BY image_id`, user, env)
	if err != nil {
		return nil, fmt.Errorf("image pulls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("image pulls: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("image pulls: %w", err)
	}
	return ids, nil
}
