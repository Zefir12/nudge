// Package hostload reads the whole machine's CPU and memory from /proc (/proc/stat and
// /proc/meminfo, which show the host's figures inside a container too), answered for an
// agent's host by its control service (GET /v1/host).
package hostload

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrUnknown means the host's load can't be told: no /proc (another platform), or an
// agent too old to answer.
var ErrUnknown = errors.New("host load unknown")

// Reading is one read of the host: the CPU time counters since boot (in clock ticks,
// summed over every CPU) and the memory, in bytes.
type Reading struct {
	CPUBusy         uint64 `json:"cpu_busy"`
	CPUTotal        uint64 `json:"cpu_total"`
	MemoryTotal     uint64 `json:"memory_total"`
	MemoryAvailable uint64 `json:"memory_available"`
}

// MemoryUsed is the memory in use: all of it but what is available without swapping
// (free memory plus the page cache the kernel can reclaim).
func (r Reading) MemoryUsed() uint64 {
	if r.MemoryAvailable > r.MemoryTotal {
		return 0
	}
	return r.MemoryTotal - r.MemoryAvailable
}

// CPUPercent is how busy the whole machine was between prev and r, 0 to 100; ok is false
// when the counters didn't advance (the same read twice, or a reboot in between).
func CPUPercent(prev, r Reading) (pct float64, ok bool) {
	if r.CPUTotal <= prev.CPUTotal || r.CPUBusy < prev.CPUBusy {
		return 0, false
	}
	pct = float64(r.CPUBusy-prev.CPUBusy) / float64(r.CPUTotal-prev.CPUTotal) * 100
	return min(pct, 100), true
}

// Read reads the host from the proc filesystem mounted at proc ("/proc"); ErrUnknown
// when it can't be read.
func Read(proc string) (Reading, error) {
	stat, err := os.Open(filepath.Join(proc, "stat")) //nolint:gosec // proc is a fixed path from the caller
	if err != nil {
		return Reading{}, fmt.Errorf("%w: %w", ErrUnknown, err)
	}
	defer func() { _ = stat.Close() }()
	meminfo, err := os.Open(filepath.Join(proc, "meminfo")) //nolint:gosec // proc is a fixed path from the caller
	if err != nil {
		return Reading{}, fmt.Errorf("%w: %w", ErrUnknown, err)
	}
	defer func() { _ = meminfo.Close() }()
	r, err := Parse(stat, meminfo)
	if err != nil {
		return Reading{}, fmt.Errorf("%w: %w", ErrUnknown, err)
	}
	return r, nil
}

// Parse reads a Reading from the contents of /proc/stat and /proc/meminfo.
func Parse(stat, meminfo io.Reader) (Reading, error) {
	var r Reading
	if err := parseStat(stat, &r); err != nil {
		return Reading{}, err
	}
	if err := parseMeminfo(meminfo, &r); err != nil {
		return Reading{}, err
	}
	return r, nil
}

// parseStat reads the aggregate "cpu" line: user nice system idle iowait irq softirq
// steal (guest time is already in user and nice). Idle and iowait are the idle time.
func parseStat(stat io.Reader, r *Reading) error {
	sc := bufio.NewScanner(stat)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if len(fields) < 5 {
			return errors.New("short cpu line in /proc/stat")
		}
		var total, idle uint64
		for i, f := range fields[1:min(len(fields), 9)] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return fmt.Errorf("cpu line in /proc/stat: %w", err)
			}
			total += v
			if i == 3 || i == 4 {
				idle += v
			}
		}
		r.CPUTotal, r.CPUBusy = total, total-idle
		return nil
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read /proc/stat: %w", err)
	}
	return errors.New("no cpu line in /proc/stat")
}

// parseMeminfo reads MemTotal and MemAvailable (in kB).
func parseMeminfo(meminfo io.Reader, r *Reading) error {
	var gotTotal, gotAvail bool
	sc := bufio.NewScanner(meminfo)
	for sc.Scan() && (!gotTotal || !gotAvail) {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || (key != "MemTotal" && key != "MemAvailable") {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
		if err != nil {
			return fmt.Errorf("%s in /proc/meminfo: %w", key, err)
		}
		if key == "MemTotal" {
			r.MemoryTotal, gotTotal = v*1024, true
		} else {
			r.MemoryAvailable, gotAvail = v*1024, true
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read /proc/meminfo: %w", err)
	}
	if !gotTotal || !gotAvail {
		return errors.New("no MemTotal or MemAvailable in /proc/meminfo")
	}
	return nil
}

// Control serves GET /v1/host on an agent's control service, a Reading of proc (503 when
// it's unknown); every other request goes to next.
func Control(proc string, next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/host", func(w http.ResponseWriter, _ *http.Request) {
		r, err := Read(proc)
		if err != nil {
			http.Error(w, "host load unknown", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r)
	})
	mux.Handle("/", next)
	return mux
}

// Remote asks an agent's control service, over a connection from dial, for a Reading of
// its host; ErrUnknown when the agent can't tell or doesn't know the request (an older
// agent).
func Remote(ctx context.Context, dial func(context.Context) (net.Conn, error)) (Reading, error) {
	hc := &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://agent/v1/host", nil)
	if err != nil {
		return Reading{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return Reading{}, fmt.Errorf("ask the agent for its host load: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusServiceUnavailable:
		return Reading{}, ErrUnknown
	default:
		return Reading{}, fmt.Errorf("ask the agent for its host load: status %d", resp.StatusCode)
	}
	var r Reading
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&r); err != nil {
		return Reading{}, fmt.Errorf("ask the agent for its host load: %w", err)
	}
	return r, nil
}
