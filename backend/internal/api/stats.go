package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/metrics"
)

// statsTimeout bounds an overview: at 8 a second, enough for a few hundred containers.
const statsTimeout = time.Minute

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
		sample, ok := metrics.ToSample(raw)
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
	metrics.Sample
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

// environmentStats answers one sample of every running container (a user's: of theirs).
func (s *server) environmentStats(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.runDocker(r.Context(), w, r, "environment stats", "", statsTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		sc, err := s.scopeOf(ctx)
		if err != nil {
			return nil, err
		}
		return s.sampleEnvironment(ctx, c, sc)
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
	sampled, skipped := metrics.SampleContainers(ctx, c, items, s.log)
	resp := environmentStats{Containers: []containerUsage{}, Skipped: skipped}
	for _, u := range sampled {
		resp.Containers = append(resp.Containers, containerUsage{ID: u.ID, Name: u.Name, Sample: u.Sample})
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
