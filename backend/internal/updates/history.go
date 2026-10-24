package updates

import (
	"context"
	"fmt"
	"time"
)

// HistoryLimit bounds the updates kept per container or stack (a stack update has a row
// per service); older ones are dropped. MaxHistory bounds an environment's rows in all,
// so targets that were removed or renamed don't pile up.
const (
	HistoryLimit = 20
	MaxHistory   = 5000
)

// Kind is what an update replaced.
type Kind string

const (
	// KindContainer is a container recreated with its pulled image.
	KindContainer Kind = "container"
	// KindStack is a stack redeployed with its pulled images, one Change per service.
	KindStack Kind = "stack"
)

// Change is one update made through nudge: the image a container or a stack's service
// ran before and after, what a rollback by hand needs.
type Change struct {
	ID          int64
	Environment int64
	Kind        Kind
	// Target is the container or stack name; Service the compose service, empty for a
	// container.
	Target     string
	Service    string
	Image      string
	OldImageID string
	// OldDigest and NewDigest are the registry digests of the images, empty when unknown.
	OldDigest  string
	NewImageID string
	NewDigest  string
	User       string
	Time       time.Time
}

// Record saves changes as one update, stamped now, and drops each target's updates
// beyond HistoryLimit and the environment's oldest rows beyond MaxHistory.
func (s *Service) Record(ctx context.Context, changes []Change) (err error) {
	if len(changes) == 0 {
		return nil
	}
	// One time for the whole update: the bound counts updates by their time.
	now := s.clock.Now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record update: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	type target struct {
		env  int64
		kind Kind
		name string
	}
	targets := map[target]bool{}
	for _, c := range changes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO update_history
			(environment_id, kind, target, service, image, old_image_id, old_digest, new_image_id, new_digest, username, time)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Environment, string(c.Kind), c.Target, c.Service, c.Image, c.OldImageID, c.OldDigest,
			c.NewImageID, c.NewDigest, c.User, now); err != nil {
			return fmt.Errorf("record update: %w", err)
		}
		targets[target{c.Environment, c.Kind, c.Target}] = true
	}
	envs := map[int64]bool{}
	for t := range targets {
		envs[t.env] = true
		if _, err = tx.ExecContext(ctx, `DELETE FROM update_history
			WHERE environment_id = ?1 AND kind = ?2 AND target = ?3 AND time < (
				SELECT min(time) FROM (SELECT DISTINCT time FROM update_history
				WHERE environment_id = ?1 AND kind = ?2 AND target = ?3 ORDER BY time DESC LIMIT ?4))`,
			t.env, string(t.kind), t.name, HistoryLimit); err != nil {
			return fmt.Errorf("record update: %w", err)
		}
	}
	for env := range envs {
		if _, err = tx.ExecContext(ctx, `DELETE FROM update_history WHERE environment_id = ?1 AND id NOT IN (
			SELECT id FROM update_history WHERE environment_id = ?1 ORDER BY id DESC LIMIT ?2)`, env, MaxHistory); err != nil {
			return fmt.Errorf("record update: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("record update: %w", err)
	}
	return nil
}

// History returns an environment's updates, newest first; kind and target, if not
// empty, narrow it to one container or stack.
func (s *Service) History(ctx context.Context, envID int64, kind Kind, target string) ([]Change, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, target, service, image, old_image_id, old_digest,
		new_image_id, new_digest, username, time FROM update_history
		WHERE environment_id = ? AND (? = '' OR kind = ?) AND (? = '' OR target = ?)
		ORDER BY id DESC LIMIT 500`, envID, string(kind), string(kind), target, target)
	if err != nil {
		return nil, fmt.Errorf("list update history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Change{}
	for rows.Next() {
		c := Change{Environment: envID}
		var t int64
		if err := rows.Scan(&c.ID, &c.Kind, &c.Target, &c.Service, &c.Image, &c.OldImageID, &c.OldDigest,
			&c.NewImageID, &c.NewDigest, &c.User, &t); err != nil {
			return nil, fmt.Errorf("list update history: %w", err)
		}
		c.Time = time.UnixMilli(t).UTC()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list update history: %w", err)
	}
	return out, nil
}
