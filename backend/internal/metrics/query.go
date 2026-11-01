package metrics

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MaxPoints bounds a history: a finer resolution over a longer span is coarsened. 48h of
// raw samples at the default interval fit.
const MaxPoints = 3000

// Scope is whose containers a query adds up: every container, or a user's (their owner
// key). A user's totals are summed from their containers' rows at query time rather
// than kept as per-owner totals: no third copy of every tick to keep in step, and a
// user's numbers are always exactly the sum of the container rows they may see.
type Scope struct {
	All   bool
	Owner string
}

// Summary is an environment's latest tick: CPU as a percentage of one CPU, memory
// without the page cache, over Containers sampled.
type Summary struct {
	SampledAt   time.Time `json:"sampled_at"`
	CPUPercent  float64   `json:"cpu_percent"`
	MemoryUsage uint64    `json:"memory_usage"`
	Containers  int       `json:"containers"`
	// Host is the whole machine's load, in a summary of every container; absent when
	// it can't be told.
	Host *Host `json:"host,omitempty"`
}

// Latest returns env's latest tick within sc; ok is false when there is none from the
// last few intervals (collection is off, or the environment went offline).
func (s *Service) Latest(ctx context.Context, env int64, sc Scope) (sum Summary, ok bool, err error) {
	if s.interval <= 0 {
		return Summary{}, false, nil
	}
	var t int64
	var cpu, mem float64
	var n int
	var hostCPU sql.NullFloat64
	var hostMem, hostTotal sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT time, cpu, memory, containers, host_cpu, host_memory, host_memory_total
		FROM environment_samples WHERE environment_id = ? ORDER BY time DESC LIMIT 1`, env).
		Scan(&t, &cpu, &mem, &n, &hostCPU, &hostMem, &hostTotal)
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, false, nil
	}
	if err != nil {
		return Summary{}, false, fmt.Errorf("latest metrics: %w", err)
	}
	at := time.Unix(t, 0).UTC()
	if s.clock.Now().Sub(at) > staleTicks*s.interval {
		return Summary{}, false, nil
	}
	if !sc.All {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cpu), 0), COALESCE(SUM(memory), 0), COUNT(*)
			FROM container_samples WHERE environment_id = ? AND time = ? AND owner = ? AND owner <> ''`,
			env, t, sc.Owner).Scan(&cpu, &mem, &n); err != nil {
			return Summary{}, false, fmt.Errorf("latest metrics: %w", err)
		}
	}
	sum = Summary{SampledAt: at, CPUPercent: cpu, MemoryUsage: uint64(mem), Containers: n}
	if sc.All && hostCPU.Valid && hostMem.Valid && hostTotal.Valid {
		sum.Host = &Host{CPUPercent: hostCPU.Float64, MemoryUsage: u64(hostMem.Int64), MemoryTotal: u64(hostTotal.Int64)}
	}
	return sum, true, nil
}

// Query asks for a history: of an environment within Scope, or of one container
// (Container set, Scope ignored), from Since on, in buckets of Step (zero: the
// collection interval for raw samples, an hour for rollups).
type Query struct {
	Env       int64
	Container string
	Scope     Scope
	Since     time.Time
	Step      time.Duration
}

// Point is one bucket of a history: CPU and memory averaged and at their maximum, the
// I/O counters at their largest.
type Point struct {
	Time        time.Time `json:"time"`
	CPUPercent  float64   `json:"cpu_percent"`
	CPUMax      float64   `json:"cpu_max"`
	MemoryUsage uint64    `json:"memory_usage"`
	MemoryMax   uint64    `json:"memory_max"`
	NetworkRx   uint64    `json:"network_rx"`
	NetworkTx   uint64    `json:"network_tx"`
	BlockRead   uint64    `json:"block_read"`
	BlockWrite  uint64    `json:"block_write"`
	// Host is the whole machine's load averaged, in an environment's history of every
	// container; absent where it wasn't told.
	Host *Host `json:"host,omitempty"`
}

// History is a query's answer: Source says whether the raw samples or the hourly
// rollups answered, Step (seconds) is the bucket size used.
type History struct {
	Source string  `json:"source"`
	Step   int64   `json:"step"`
	Points []Point `json:"points"`
}

// Sources of a History.
const (
	SourceRaw    = "raw"
	SourceHourly = "hourly"
)

// History answers q. Raw samples answer while Since is within RawRetention and Step is
// under an hour, else the hourly rollups (Step at least an hour, in whole hours). Since
// is clamped to the retention, and Step coarsened to at most MaxPoints buckets.
func (s *Service) History(ctx context.Context, q Query) (History, error) {
	now := s.clock.Now()
	if oldest := now.Add(-s.retention); q.Since.Before(oldest) {
		q.Since = oldest
	}
	source := SourceRaw
	if q.Since.Before(now.Add(-RawRetention)) || q.Step >= time.Hour {
		source = SourceHourly
	}
	step := int64(q.Step / time.Second)
	if source == SourceHourly {
		step = max(1, (step+3599)/3600) * 3600
	} else if step < 1 {
		step = int64(cmp.Or(s.interval, DefaultInterval) / time.Second)
	}
	if span := int64(now.Sub(q.Since) / time.Second); span/step > MaxPoints {
		step = (span + MaxPoints - 1) / MaxPoints
		if source == SourceHourly {
			step = (step + 3599) / 3600 * 3600
		}
	}
	since := q.Since.Unix()
	if source == SourceHourly {
		since = q.Since.Truncate(time.Hour).Unix()
	}
	query, args := historyQuery(source, q, since)
	rows, err := s.db.QueryContext(ctx, query, append([]any{step}, args...)...)
	if err != nil {
		return History{}, fmt.Errorf("metrics history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	h := History{Source: source, Step: step, Points: []Point{}}
	for rows.Next() {
		var (
			p              Point
			b              int64
			mem, memMax    float64
			rx, tx, br, bw int64
			hostCPU        sql.NullFloat64
			hostMem        sql.NullFloat64
			hostTotal      sql.NullInt64
		)
		if err := rows.Scan(&b, &p.CPUPercent, &p.CPUMax, &mem, &memMax, &rx, &tx, &br, &bw, &hostCPU, &hostMem, &hostTotal); err != nil {
			return History{}, fmt.Errorf("metrics history: %w", err)
		}
		p.Time = time.Unix(b, 0).UTC()
		p.MemoryUsage, p.MemoryMax = uint64(mem), uint64(memMax)
		p.NetworkRx, p.NetworkTx, p.BlockRead, p.BlockWrite = u64(rx), u64(tx), u64(br), u64(bw)
		if hostCPU.Valid && hostMem.Valid && hostTotal.Valid {
			p.Host = &Host{CPUPercent: hostCPU.Float64, MemoryUsage: uint64(max(hostMem.Float64, 0)), MemoryTotal: u64(hostTotal.Int64)}
		}
		h.Points = append(h.Points, p)
	}
	if err := rows.Err(); err != nil {
		return History{}, fmt.Errorf("metrics history: %w", err)
	}
	return h, nil
}

// u64 reads back a counter stored by i64; none is negative.
func u64(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// historyOuter and historyEnd wrap an inner query that selects (t, cpu, cpu_max, mem,
// mem_max, rx, tx, br, bw, n, hcpu, hmem, htotal, hn), n weighing the averages by the
// samples behind each row and hn the host's (NULL host columns and hn 0 where there is
// none), into buckets of ?1 seconds. The queries below are built from constants only.
const (
	historyOuter = `SELECT t / ?1 * ?1 AS b, SUM(cpu * n) / SUM(n), MAX(cpu_max), SUM(mem * n) / SUM(n), MAX(mem_max),
			MAX(rx), MAX(tx), MAX(br), MAX(bw),
			SUM(hcpu * hn) / NULLIF(SUM(hn), 0), SUM(hmem * hn) / NULLIF(SUM(hn), 0), MAX(htotal) FROM (`
	historyEnd = `) GROUP BY b ORDER BY b`

	// noHost are the host columns of the queries of containers, which have none.
	noHost = `, NULL AS hcpu, NULL AS hmem, NULL AS htotal, 0 AS hn`

	rawColumns = `SELECT time AS t, cpu, cpu AS cpu_max, memory AS mem, memory AS mem_max, network_rx AS rx, network_tx AS tx,
			block_read AS br, block_write AS bw, 1 AS n`
	hourColumns = `SELECT hour AS t, cpu_avg AS cpu, cpu_max, memory_avg AS mem, memory_max AS mem_max, network_rx AS rx, network_tx AS tx,
			block_read AS br, block_write AS bw, samples AS n`
	envRawHost  = `, host_cpu AS hcpu, host_memory AS hmem, host_memory_total AS htotal, host_cpu IS NOT NULL AS hn`
	envHourHost = `, host_cpu AS hcpu, host_memory AS hmem, host_memory_total AS htotal, host_samples AS hn`

	containerRawQuery = historyOuter + rawColumns + noHost +
		` FROM container_samples WHERE environment_id = ?2 AND container_id = ?3 AND time >= ?4` + historyEnd
	containerHourQuery = historyOuter + hourColumns + noHost +
		` FROM container_hours WHERE environment_id = ?2 AND container_id = ?3 AND hour >= ?4` + historyEnd
	envRawQuery = historyOuter + rawColumns + envRawHost +
		` FROM environment_samples WHERE environment_id = ?2 AND time >= ?3` + historyEnd
	envHourQuery = historyOuter + hourColumns + envHourHost +
		` FROM environment_hours WHERE environment_id = ?2 AND hour >= ?3` + historyEnd
	// A user's tick is the sum of their containers' rows of that tick.
	ownerRawQuery = historyOuter + `SELECT time AS t, SUM(cpu) AS cpu, SUM(cpu) AS cpu_max, SUM(memory) AS mem, SUM(memory) AS mem_max,
			SUM(network_rx) AS rx, SUM(network_tx) AS tx, SUM(block_read) AS br, SUM(block_write) AS bw, 1 AS n` + noHost + `
		FROM container_samples WHERE environment_id = ?2 AND owner = ?3 AND owner <> '' AND time >= ?4
		GROUP BY time` + historyEnd
	// Summed maximums are an upper bound: the containers peaked at different times.
	ownerHourQuery = historyOuter + `SELECT hour AS t, SUM(cpu_avg) AS cpu, SUM(cpu_max) AS cpu_max, SUM(memory_avg) AS mem, SUM(memory_max) AS mem_max,
			SUM(network_rx) AS rx, SUM(network_tx) AS tx, SUM(block_read) AS br, SUM(block_write) AS bw, MAX(samples) AS n` + noHost + `
		FROM container_hours WHERE environment_id = ?2 AND owner = ?3 AND owner <> '' AND hour >= ?4
		GROUP BY hour` + historyEnd
)

// historyQuery picks q's query on source; its parameters after the step (?1).
func historyQuery(source string, q Query, since int64) (string, []any) {
	raw := source == SourceRaw
	switch {
	case q.Container != "" && raw:
		return containerRawQuery, []any{q.Env, q.Container, since}
	case q.Container != "":
		return containerHourQuery, []any{q.Env, q.Container, since}
	case q.Scope.All && raw:
		return envRawQuery, []any{q.Env, since}
	case q.Scope.All:
		return envHourQuery, []any{q.Env, since}
	case raw:
		return ownerRawQuery, []any{q.Env, q.Scope.Owner, since}
	default:
		return ownerHourQuery, []any{q.Env, q.Scope.Owner, since}
	}
}
