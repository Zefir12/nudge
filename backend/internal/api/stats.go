package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

const (
	// maxConcurrentStats bounds the one-shot stats calls an environment overview runs at
	// once; each takes about a second on the daemon (it waits for a second sample).
	maxConcurrentStats = 8
	// statsTimeout bounds an overview: at 8 a second, enough for a few hundred containers.
	statsTimeout = time.Minute
)

// statsSample is one container's usage, computed from the daemon's raw counters. CPU
// is a percentage of one CPU, so a busy container on four CPUs can reach 400. Memory
// leaves out the page cache, like `docker stats`; the limit is the host's memory when
// the container has none. Network and block I/O are totals since the container started.
type statsSample struct {
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

// toStatsSample computes a sample; ok is false when the response has no previous CPU
// reading to compute the CPU percentage from (the first frame of a stream).
func toStatsSample(s container.StatsResponse) (statsSample, bool) {
	out := statsSample{Time: s.Read.UTC(), PIDs: s.PidsStats.Current}
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	if s.OSType == "windows" {
		if s.PreRead.IsZero() {
			return statsSample{}, false
		}
		// Windows counts CPU time in 100ns units across all processors.
		possible := float64(s.Read.Sub(s.PreRead).Nanoseconds()) / 100 * float64(s.NumProcs)
		if possible > 0 && cpuDelta > 0 {
			out.CPUPercent = cpuDelta / possible * 100
		}
		out.MemoryUsage = s.MemoryStats.PrivateWorkingSet
	} else {
		if s.PreCPUStats.SystemUsage == 0 {
			return statsSample{}, false
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

// containerStats streams a running container's usage over a WebSocket, one sample per
// daemon reading (about every second). The container is checked before the upgrade, so
// a missing or stopped one is a plain JSON error.
func (s *server) containerStats(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	ictx, icancel := context.WithTimeout(r.Context(), dockerTimeout)
	ct, ref, err := s.ownedContainer(ictx, c, id)
	icancel()
	if docker.NotFound(err) {
		s.writeError(w, http.StatusNotFound, "container not found")
		return
	}
	if err != nil {
		s.dockerError(w, r, env, "container stats", err)
		return
	}
	s.envs.Seen(r.Context(), &env)
	id = ref
	if ct.State == nil || !ct.State.Running {
		s.writeError(w, http.StatusConflict, "container is not running")
		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Warn("container stats: websocket upgrade", "env", env.ID, "container", id, "err", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx, cancel := context.WithCancel(ws.CloseRead(r.Context()))
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	wg.Go(func() { keepAlive(ctx, cancel, ws) })

	stats, err := c.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		s.endStream(ctx, ws, "stats", env.ID, id, err)
		return
	}
	defer func() { _ = stats.Body.Close() }()
	dec := json.NewDecoder(stats.Body)
	for {
		var raw container.StatsResponse
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			s.endStream(ctx, ws, "stats", env.ID, id, err)
			return
		}
		sample, ok := toStatsSample(raw)
		if !ok {
			continue
		}
		if err := wsjson.Write(ctx, ws, sample); err != nil {
			return
		}
	}
}

// containerUsage is one running container's sample in an environment overview.
type containerUsage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	statsSample
}

// usageTotals adds up the overview's containers.
type usageTotals struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryUsage uint64  `json:"memory_usage"`
	NetworkRx   uint64  `json:"network_rx"`
	NetworkTx   uint64  `json:"network_tx"`
	BlockRead   uint64  `json:"block_read"`
	BlockWrite  uint64  `json:"block_write"`
}

// environmentStats lists the sampled containers; Skipped counts those that could not be
// sampled (they stopped meanwhile, or the overview ran out of time).
type environmentStats struct {
	Containers []containerUsage `json:"containers"`
	Skipped    int              `json:"skipped"`
	Total      usageTotals      `json:"total"`
}

// environmentStats answers one sample of every running container (a user's: of theirs)
// and keeps the totals as the environment's summary.
func (s *server) environmentStats(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.runDocker(r.Context(), w, r, "environment stats", "", statsTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		sc, err := s.scopeOf(ctx)
		if err != nil {
			return nil, err
		}
		stats, err := s.sampleEnvironment(ctx, c, sc)
		if err != nil {
			return nil, err
		}
		if id, err := strconv.ParseInt(r.PathValue("env"), 10, 64); err == nil && incomplete(ctx, stats) == nil {
			s.storeSummary(summaryKey{env: id, owner: sc.key}, stats)
		}
		return stats, nil
	})
	if ok {
		s.writeJSON(w, http.StatusOK, resp)
	}
}

// sampleEnvironment takes one sample of every running container sc owns, sorted by name.
func (s *server) sampleEnvironment(ctx context.Context, c docker.Client, sc scope) (environmentStats, error) {
	list, err := c.ContainerList(ctx, client.ContainerListOptions{Filters: sc.filters(nil)})
	if err != nil {
		return environmentStats{}, err
	}
	items, err := sc.ownedContainers(ctx, c, list.Items)
	if err != nil {
		return environmentStats{}, err
	}
	usage := make([]*containerUsage, len(items))
	sem := make(chan struct{}, maxConcurrentStats)
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
			sample, err := oneSample(ctx, c, item.ID)
			if err != nil {
				s.log.Debug("environment stats: container", "container", item.ID, "err", err)
				return
			}
			u := containerUsage{ID: item.ID, statsSample: sample}
			if len(item.Names) > 0 {
				u.Name = strings.TrimPrefix(item.Names[0], "/")
			}
			usage[i] = &u
		})
	}
	wg.Wait()
	resp := environmentStats{Containers: []containerUsage{}}
	for _, u := range usage {
		if u == nil {
			resp.Skipped++
			continue
		}
		resp.Containers = append(resp.Containers, *u)
		resp.Total.CPUPercent += u.CPUPercent
		resp.Total.MemoryUsage += u.MemoryUsage
		resp.Total.NetworkRx += u.NetworkRx
		resp.Total.NetworkTx += u.NetworkTx
		resp.Total.BlockRead += u.BlockRead
		resp.Total.BlockWrite += u.BlockWrite
	}
	slices.SortFunc(resp.Containers, func(a, b containerUsage) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return resp, nil
}

// oneSample asks the daemon for a sample with a previous reading, so the CPU percentage
// can be computed.
func oneSample(ctx context.Context, c docker.Client, id string) (statsSample, error) {
	stats, err := c.ContainerStats(ctx, id, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return statsSample{}, err
	}
	defer func() { _ = stats.Body.Close() }()
	var raw container.StatsResponse
	if err := json.NewDecoder(stats.Body).Decode(&raw); err != nil {
		return statsSample{}, err
	}
	sample, ok := toStatsSample(raw)
	if !ok {
		return statsSample{}, errors.New("no previous CPU reading")
	}
	return sample, nil
}
