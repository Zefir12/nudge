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

// MaxConcurrentStats bounds the one-shot stats calls a sampling of one environment runs
// at once; each takes about a second on the daemon (it waits for a second reading).
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

// SampleOne asks the daemon for a sample with a previous reading, so the CPU percentage
// can be computed.
func SampleOne(ctx context.Context, c docker.Client, id string) (Sample, error) {
	stats, err := c.ContainerStats(ctx, id, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return Sample{}, err
	}
	defer func() { _ = stats.Body.Close() }()
	var raw container.StatsResponse
	if err := json.NewDecoder(stats.Body).Decode(&raw); err != nil {
		return Sample{}, err
	}
	sample, ok := ToSample(raw)
	if !ok {
		return Sample{}, errors.New("no previous CPU reading")
	}
	return sample, nil
}

// Container is one listed container's sample.
type Container struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Sample
}

// SampleContainers takes one sample of each of items, MaxConcurrentStats at once, in
// the order listed; skipped counts those that could not be sampled (they stopped
// meanwhile, or ctx ended).
func SampleContainers(ctx context.Context, c docker.Client, items []container.Summary, log *slog.Logger) (sampled []Container, skipped int) {
	out := make([]*Container, len(items))
	sem := make(chan struct{}, MaxConcurrentStats)
	var wg sync.WaitGroup
sample:
	for i, item := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break sample
		}
		wg.Go(func() {
			defer func() { <-sem }()
			sample, err := SampleOne(ctx, c, item.ID)
			if err != nil {
				log.Debug("container stats", "container", item.ID, "err", err)
				return
			}
			u := Container{ID: item.ID, Sample: sample}
			if len(item.Names) > 0 {
				u.Name = strings.TrimPrefix(item.Names[0], "/")
			}
			out[i] = &u
		})
	}
	wg.Wait()
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
