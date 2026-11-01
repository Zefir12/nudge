package environments

import (
	"context"
	"fmt"
	"slices"
)

// Access returns the environments user may use, by id. Admins use every environment
// whatever this says; the API decides that.
func (s *Service) Access(ctx context.Context, user int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT environment_id FROM user_environments WHERE user_id = ? ORDER BY environment_id`, user)
	if err != nil {
		return nil, fmt.Errorf("environment access: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("environment access: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("environment access: %w", err)
	}
	return ids, nil
}

// HasAccess reports whether user may use environment env.
func (s *Service) HasAccess(ctx context.Context, user, env int64) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_environments WHERE user_id = ? AND environment_id = ?)`, user, env).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("environment access: %w", err)
	}
	return ok, nil
}

// SetAccess replaces the environments user may use with envs; an id that names no
// environment is ErrNotFound and changes nothing. The user must exist.
func (s *Service) SetAccess(ctx context.Context, user int64, envs []int64) error {
	envs = slices.Clone(envs)
	slices.Sort(envs)
	envs = slices.Compact(envs)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set environment access: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_environments WHERE user_id = ?`, user); err != nil {
		return fmt.Errorf("set environment access: %w", err)
	}
	for _, env := range envs {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO user_environments (user_id, environment_id)
			SELECT ?, id FROM environments WHERE id = ?`, user, env)
		if err != nil {
			return fmt.Errorf("set environment access: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("set environment access: %w", err)
		} else if n == 0 {
			return fmt.Errorf("environment %d: %w", env, ErrNotFound)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set environment access: %w", err)
	}
	return nil
}
