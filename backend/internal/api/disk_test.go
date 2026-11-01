package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/hostdisk"
	"git.zefirlabs.net/zefir/nudge/backend/internal/metrics"
)

func TestEnvironmentDisk(t *testing.T) {
	t.Parallel()
	var svc *metrics.Service
	withM := withMetrics(t, &metricsEnvs{fake: &dockertest.Fake{}}, &svc)
	et := newEnvTestOpts(t, map[string]*dockertest.Fake{"local": {}, "down": {}}, func(o *Options) {
		withM(o)
		o.DiskSpace = func(_ context.Context, env environments.Environment) (hostdisk.Space, error) {
			if env.Name == "down" {
				return hostdisk.Space{}, hostdisk.ErrUnknown
			}
			return hostdisk.Space{Total: 1000, Free: 400}, nil
		}
	})
	local, down := et.ids["local"], et.ids["down"]
	path := func(id int64) string { return fmt.Sprintf("/api/v1/environments/%d/disk", id) }

	// Before a measurement: the disk's size alone.
	got := getJSON[diskResponse](t, et.h, path(local), et.cookie)
	if got.Usage != nil || got.Disk == nil || *got.Disk != (hostdisk.Space{Total: 1000, Free: 400}) {
		t.Errorf("before a measurement: %+v, want no usage and the disk", got)
	}

	d := metrics.Disk{MeasuredAt: metricsNow, Volumes: metrics.DiskPart{Size: 100, Count: 2, Active: 1, Reclaimable: 40}, Images: metrics.DiskPart{Size: 300}}
	if err := svc.SaveDisk(context.Background(), local, d); err != nil {
		t.Fatal(err)
	}
	got = getJSON[diskResponse](t, et.h, path(local), et.cookie)
	if got.Usage == nil || !got.Usage.MeasuredAt.Equal(metricsNow) || got.Usage.Volumes != d.Volumes || got.Usage.Images != d.Images {
		t.Errorf("usage = %+v, want %+v", got.Usage, d)
	}

	// An unknown size (no mount, an older agent) is null.
	if got := getJSON[diskResponse](t, et.h, path(down), et.cookie); got.Disk != nil || got.Usage != nil {
		t.Errorf("unknown disk: %+v, want nulls", got)
	}
	if rec := do(t, et.h, request{method: "GET", path: path(999), cookie: et.cookie}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown environment: code = %d, want 404", rec.Code)
	}
	if rec := do(t, et.h, request{method: "GET", path: path(local)}); rec.Code != http.StatusUnauthorized {
		t.Errorf("without session: code = %d, want 401", rec.Code)
	}
}

// Measuring on demand works once a minute.
func TestMeasureDisk(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": {}, "down": {Resources: dockertest.Resources{
		DiskUsageErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")},
	}}})
	path := func(id int64) string { return fmt.Sprintf("/api/v1/environments/%d/disk", id) }
	if rec := do(t, et.h, request{method: "POST", path: path(et.ids["local"]), cookie: et.cookie}); rec.Code != http.StatusNoContent {
		t.Fatalf("measure: code = %d (%s)", rec.Code, rec.Body)
	}
	rec := do(t, et.h, request{method: "POST", path: path(et.ids["local"]), cookie: et.cookie})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
		t.Errorf("measure again at once: code = %d, Retry-After %q; want 429, 60", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := do(t, et.h, request{method: "POST", path: path(et.ids["down"]), cookie: et.cookie}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("offline: code = %d, want 503", rec.Code)
	}
}

// With no DiskSpace the size is unknown.
func TestEnvironmentDiskNoSpace(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": {}})
	got := getJSON[diskResponse](t, et.h, fmt.Sprintf("/api/v1/environments/%d/disk", et.ids["local"]), et.cookie)
	if got.Disk != nil || got.Usage != nil {
		t.Errorf("got %+v, want nulls", got)
	}
}
