package metrics

import (
	"context"
	"fmt"
	"time"
)

// DiskPart is what one kind of Docker object takes on disk.
type DiskPart struct {
	// Size is in bytes; Reclaimable is what a prune of the unused ones would free.
	Size        int64 `json:"size"`
	Reclaimable int64 `json:"reclaimable"`
	// Count is how many there are, Active how many are in use.
	Count  int64 `json:"count"`
	Active int64 `json:"active"`
}

// Disk is an environment's last `docker system df`.
type Disk struct {
	MeasuredAt time.Time `json:"measured_at"`
	Volumes    DiskPart  `json:"volumes"`
	Images     DiskPart  `json:"images"`
	BuildCache DiskPart  `json:"build_cache"`
	Containers DiskPart  `json:"containers"`
}

// parts are d's kinds by their stored name.
func (d *Disk) parts() map[string]*DiskPart {
	return map[string]*DiskPart{
		"volumes": &d.Volumes, "images": &d.Images, "build_cache": &d.BuildCache, "containers": &d.Containers,
	}
}

// SaveDisk replaces env's last disk usage with d.
func (s *Service) SaveDisk(ctx context.Context, env int64, d Disk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save disk usage: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM disk_usage WHERE environment_id = ?`, env); err != nil {
		return fmt.Errorf("save disk usage: %w", err)
	}
	for kind, p := range d.parts() {
		if _, err := tx.ExecContext(ctx, `INSERT INTO disk_usage (environment_id, kind, size, reclaimable, total_count, active_count, measured_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			env, kind, p.Size, p.Reclaimable, p.Count, p.Active, d.MeasuredAt.UnixMilli()); err != nil {
			return fmt.Errorf("save disk usage: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save disk usage: %w", err)
	}
	return nil
}

// Disk returns env's last disk usage; false when it wasn't measured yet.
func (s *Service) Disk(ctx context.Context, env int64) (Disk, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, size, reclaimable, total_count, active_count, measured_at FROM disk_usage WHERE environment_id = ?`, env)
	if err != nil {
		return Disk{}, false, fmt.Errorf("disk usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var (
		d     Disk
		found bool
	)
	parts := d.parts()
	for rows.Next() {
		var (
			kind string
			p    DiskPart
			at   int64
		)
		if err := rows.Scan(&kind, &p.Size, &p.Reclaimable, &p.Count, &p.Active, &at); err != nil {
			return Disk{}, false, fmt.Errorf("disk usage: %w", err)
		}
		if dst, ok := parts[kind]; ok {
			*dst = p
			d.MeasuredAt = time.UnixMilli(at)
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return Disk{}, false, fmt.Errorf("disk usage: %w", err)
	}
	return d, found, nil
}
