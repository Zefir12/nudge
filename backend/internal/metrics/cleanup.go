package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Cleanup rolls the finished hours of raw samples up into hourly rows, drops raw samples
// older than RawRetention and rollups and activity counts older than the retention, and
// drops every row of an environment not in envs (removed ones), all in one transaction.
//
// Each run rolls up again from the newest hour already rolled up, so samples a tick
// stored after the last run (a tick that straddled the hour) are counted; REPLACE
// makes that idempotent.
func (s *Service) Cleanup(ctx context.Context, now time.Time, envs []int64) error {
	ids, err := json.Marshal(envs)
	if err != nil {
		return fmt.Errorf("metrics cleanup: %w", err)
	}
	current := now.Truncate(time.Hour).Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metrics cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []struct {
		what string
		sql  string
		args []any
	}{
		{"drop removed environments", `DELETE FROM environment_samples WHERE environment_id NOT IN (SELECT value FROM json_each(?))`, []any{string(ids)}},
		{"drop removed environments", `DELETE FROM container_samples WHERE environment_id NOT IN (SELECT value FROM json_each(?))`, []any{string(ids)}},
		{"drop removed environments", `DELETE FROM environment_hours WHERE environment_id NOT IN (SELECT value FROM json_each(?))`, []any{string(ids)}},
		{"drop removed environments", `DELETE FROM container_hours WHERE environment_id NOT IN (SELECT value FROM json_each(?))`, []any{string(ids)}},
		{"drop removed environments", `DELETE FROM disk_usage WHERE environment_id NOT IN (SELECT value FROM json_each(?))`, []any{string(ids)}},
		{"drop removed environments", `DELETE FROM activity_hours WHERE environment_id NOT IN (SELECT value FROM json_each(?))`, []any{string(ids)}},
		{"roll up environments", `INSERT OR REPLACE INTO environment_hours
			(environment_id, hour, cpu_avg, cpu_max, memory_avg, memory_max, network_rx, network_tx, block_read, block_write, containers, samples,
				host_cpu, host_memory, host_memory_total, host_samples)
			SELECT environment_id, time / 3600 * 3600 AS h, AVG(cpu), MAX(cpu), AVG(memory), MAX(memory),
				MAX(network_rx), MAX(network_tx), MAX(block_read), MAX(block_write), MAX(containers), COUNT(*),
				AVG(host_cpu), AVG(host_memory), MAX(host_memory_total), COUNT(host_cpu)
			FROM environment_samples
			WHERE time >= COALESCE((SELECT MAX(hour) FROM environment_hours), 0) AND time < ?
			GROUP BY environment_id, h`, []any{current}},
		{"roll up containers", `INSERT OR REPLACE INTO container_hours
			(environment_id, container_id, hour, name, owner, cpu_avg, cpu_max, memory_avg, memory_max, network_rx, network_tx, block_read, block_write, samples)
			SELECT environment_id, container_id, time / 3600 * 3600 AS h, MAX(name), MAX(owner), AVG(cpu), MAX(cpu), AVG(memory), MAX(memory),
				MAX(network_rx), MAX(network_tx), MAX(block_read), MAX(block_write), COUNT(*)
			FROM container_samples
			WHERE time >= COALESCE((SELECT MAX(hour) FROM container_hours), 0) AND time < ?
			GROUP BY environment_id, container_id, h`, []any{current}},
		{"drop raw samples", `DELETE FROM environment_samples WHERE time < ?`, []any{now.Add(-RawRetention).Unix()}},
		{"drop raw samples", `DELETE FROM container_samples WHERE time < ?`, []any{now.Add(-RawRetention).Unix()}},
		{"drop rollups", `DELETE FROM environment_hours WHERE hour < ?`, []any{now.Add(-s.retention).Unix()}},
		{"drop rollups", `DELETE FROM container_hours WHERE hour < ?`, []any{now.Add(-s.retention).Unix()}},
		{"drop activity", `DELETE FROM activity_hours WHERE hour < ?`, []any{now.Add(-s.retention).Unix()}},
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.sql, st.args...); err != nil {
			return fmt.Errorf("metrics cleanup: %s: %w", st.what, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metrics cleanup: %w", err)
	}
	return nil
}
