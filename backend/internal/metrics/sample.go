package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// MaxConcurrentStats bounds the stats calls a sampling of one environment runs at once.
// One with a previous reading takes about a second on the daemon (it waits for a second
// reading); a one-shot call answers at once.
const MaxConcurrentStats = 8

// Sample is one container's usage, computed from the daemon's raw counters. CPU is a
// percentage of one CPU, so a busy container on four CPUs can reach 400. Memory leaves
// out the page cache, like `docker stats`; the limit is the host's memory when the
// container has none. Network and block I/O are totals since the container started.
type Sample struct {
	Time          time.Time `json:"time"`
	CPUPercent    float64   `json:"cpu_percent"`
	MemoryUsage   uint64    `json:"memory_usage"`
	MemoryLimit   uint64    `json:"memory_limit"`
	MemoryPercent float64   `json:"memory_percent"`
	NetworkRx     uint64    `json:"network_rx"`
	NetworkTx     uint64    `json:"network_tx"`
	BlockRead     uint64    `json:"block_read"`
	BlockWrite    uint64    `json:"block_write"`
	PIDs          uint64    `json:"pids"`
}

// ToSample computes a sample; ok is false when the response has no previous CPU reading
// to compute the CPU percentage from (the first frame of a stream).
func ToSample(s container.StatsResponse) (Sample, bool) {
	out := Sample{Time: s.Read.UTC(), PIDs: s.PidsStats.Current}
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	if s.OSType == "windows" {
		if s.PreRead.IsZero() {
			return Sample{}, false
		}
		// Windows counts CPU time in 100ns units across all processors.
		possible := float64(s.Read.Sub(s.PreRead).Nanoseconds()) / 100 * float64(s.NumProcs)
		if possible > 0 && cpuDelta > 0 {
			out.CPUPercent = cpuDelta / possible * 100
		}
		out.MemoryUsage = s.MemoryStats.PrivateWorkingSet
	} else {
		if s.PreCPUStats.SystemUsage == 0 {
			return Sample{}, false
		}
		systemDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
		cpus := float64(s.CPUStats.OnlineCPUs)
		if cpus == 0 {
			cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
		}
		if systemDelta > 0 && cpuDelta > 0 {
			out.CPUPercent = cpuDelta / systemDelta * cpus * 100
		}
		out.MemoryUsage = memoryWithoutCache(s.MemoryStats)
		out.MemoryLimit = s.MemoryStats.Limit
		if out.MemoryLimit > 0 {
			out.MemoryPercent = float64(out.MemoryUsage) / float64(out.MemoryLimit) * 100
		}
	}
	for _, n := range s.Networks {
		out.NetworkRx += n.RxBytes
		out.NetworkTx += n.TxBytes
	}
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			out.BlockRead += e.Value
		case "write":
			out.BlockWrite += e.Value
		}
	}
	if s.OSType == "windows" {
		out.BlockRead, out.BlockWrite = s.StorageStats.ReadSizeBytes, s.StorageStats.WriteSizeBytes
	}
	return out, true
}

// memoryWithoutCache subtracts the inactive page cache, which the kernel reclaims under
// pressure: total_inactive_file on cgroup v1, inactive_file on v2.
func memoryWithoutCache(m container.MemoryStats) uint64 {
	for _, key := range []string{"total_inactive_file", "inactive_file"} {
		if v, ok := m.Stats[key]; ok && v < m.Usage {
			return m.Usage - v
		}
	}
	return m.Usage
}

// toSampleSince computes a sample of cur, a one-shot reading, with prev, an earlier
// reading of the same container, as its previous one: the CPU percentage covers the time
// between them. ok is false as ToSample's, and when the container's CPU counter went
// back (it restarted since prev).
func toSampleSince(prev, cur container.StatsResponse) (Sample, bool) {
	if cur.CPUStats.CPUUsage.TotalUsage < prev.CPUStats.CPUUsage.TotalUsage {
		return Sample{}, false
	}
	cur.PreRead, cur.PreCPUStats = prev.Read, prev.CPUStats
	return ToSample(cur)
}

// readStats reads one stats response of container id; previous asks the daemon for a
// previous reading too, which costs it a second.
func readStats(ctx context.Context, c docker.Client, id string, previous bool) (container.StatsResponse, error) {
	stats, err := c.ContainerStats(ctx, id, client.ContainerStatsOptions{IncludePreviousSample: previous})
	if err != nil {
		return container.StatsResponse{}, err
	}
	defer func() { _ = stats.Body.Close() }()
	var raw container.StatsResponse
	if err := json.NewDecoder(stats.Body).Decode(&raw); err != nil {
		return container.StatsResponse{}, err
	}
	return raw, nil
}

// errNoPrevious is a sample without a previous CPU reading to compute it from.
var errNoPrevious = errors.New("no previous CPU reading")

// SampleOne asks the daemon for a sample with a previous reading, so the CPU percentage
// can be computed.
func SampleOne(ctx context.Context, c docker.Client, id string) (Sample, error) {
	raw, err := readStats(ctx, c, id, true)
	if err != nil {
		return Sample{}, err
	}
	sample, ok := ToSample(raw)
	if !ok {
		return Sample{}, errNoPrevious
	}
	return sample, nil
}

// Container is one listed container's sample.
type Container struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Sample
}

// newContainer is item's sample.
func newContainer(item container.Summary, s Sample) Container {
	u := Container{ID: item.ID, Sample: s}
	if len(item.Names) > 0 {
		u.Name = strings.TrimPrefix(item.Names[0], "/")
	}
	return u
}

// eachContainer calls fn with each of items and its index, MaxConcurrentStats at once,
// and returns when they are done; once ctx ends it starts no more.
func eachContainer(ctx context.Context, items []container.Summary, fn func(i int, item container.Summary)) {
	sem := make(chan struct{}, MaxConcurrentStats)
	var wg sync.WaitGroup
	defer wg.Wait()
	for i, item := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Go(func() {
			defer func() { <-sem }()
			fn(i, item)
		})
	}
}

// SampleContainers takes one sample of each of items, MaxConcurrentStats at once, in
// the order listed; skipped counts those that could not be sampled (they stopped
// meanwhile, or ctx ended).
func SampleContainers(ctx context.Context, c docker.Client, items []container.Summary, log *slog.Logger) (sampled []Container, skipped int) {
	out := make([]*Container, len(items))
	eachContainer(ctx, items, func(i int, item container.Summary) {
		sample, err := SampleOne(ctx, c, item.ID)
		if err != nil {
			log.Debug("container stats", "container", item.ID, "err", err)
			return
		}
		u := newContainer(item, sample)
		out[i] = &u
	})
	sampled = []Container{}
	for _, u := range out {
		if u == nil {
			skipped++
			continue
		}
		sampled = append(sampled, *u)
	}
	return sampled, skipped
}

// statsReadings are a collector's previous CPU readings of each container it sampled: of
// the last tick, which a live tick's CPU is measured from, and of the last stored tick,
// which a stored tick's is (like hostReadings). With one, a container is read one-shot,
// without the daemon's second's wait. Both hold only the containers of their tick, so
// they are bounded by what runs.
type statsReadings struct {
	last, saved map[string]container.StatsResponse
}

// cpuReading keeps of raw what a later CPU percentage is computed from.
func cpuReading(raw container.StatsResponse) container.StatsResponse {
	cpu := raw.CPUStats
	cpu.CPUUsage.PercpuUsage = nil
	return container.StatsResponse{Read: raw.Read, CPUStats: cpu}
}

// sampledTick is a collector's sampling of the listed containers: live and, when it is
// stored, stored hold the same containers in the order listed; stored's CPU covers the
// time since the last stored tick.
type sampledTick struct {
	live, stored []Container
	skipped      int
	// readings are the new CPU readings, by container ID.
	readings map[string]container.StatsResponse
}

// sampleTick samples each of items for a collector tick. A container with a reading in
// prev.last is read one-shot and its CPU measured from that reading; one without, or
// whose reading is of no use, is read with the daemon's previous reading. When save is
// set, stored has the CPU since prev.saved where there is one, else the live figure.
func sampleTick(ctx context.Context, c docker.Client, items []container.Summary, prev *statsReadings, save bool, log *slog.Logger) sampledTick {
	type result struct {
		live, stored Sample
		raw          container.StatsResponse
	}
	out := make([]*result, len(items))
	eachContainer(ctx, items, func(i int, item container.Summary) {
		var (
			raw    container.StatsResponse
			sample Sample
			ok     bool
			err    error
		)
		if p, seen := prev.last[item.ID]; seen {
			if raw, err = readStats(ctx, c, item.ID, false); err == nil {
				sample, ok = toSampleSince(p, raw)
			}
		}
		if !ok && err == nil {
			if raw, err = readStats(ctx, c, item.ID, true); err == nil {
				if sample, ok = ToSample(raw); !ok {
					err = errNoPrevious
				}
			}
		}
		if err != nil {
			log.Debug("container stats", "container", item.ID, "err", err)
			return
		}
		r := &result{live: sample, stored: sample, raw: raw}
		if p, seen := prev.saved[item.ID]; save && seen {
			if s, ok := toSampleSince(p, raw); ok {
				r.stored = s
			}
		}
		out[i] = r
	})
	t := sampledTick{live: []Container{}, readings: map[string]container.StatsResponse{}}
	if save {
		t.stored = []Container{}
	}
	for i, r := range out {
		if r == nil {
			t.skipped++
			continue
		}
		t.live = append(t.live, newContainer(items[i], r.live))
		if save {
			t.stored = append(t.stored, newContainer(items[i], r.stored))
		}
		t.readings[items[i].ID] = cpuReading(r.raw)
	}
	return t
}
