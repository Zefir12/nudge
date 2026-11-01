package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"

	"git.zefirlabs.net/zefir/nudge/backend/internal/allowances"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

const mib = 1 << 20

// allowanceTest is newOwnTest with bob's allowance in local at 1 CPU, 1 GiB, 1 GiB of
// disk, 3 containers and ports 8000-8001. He has "mine" running (0.5 CPU, 256 MiB) and
// "idle" stopped (0.25 CPU, 256 MiB); alice's container publishes port 8000.
type allowanceTest struct {
	ownTest
	s     *server
	allow allowances.Allowance
}

func newAllowanceTest(t *testing.T) allowanceTest {
	t.Helper()
	ot := newOwnTest(t)
	at := allowanceTest{ownTest: ot, allow: allowances.Allowance{
		CPUsMilli: 1000, MemoryBytes: 1 << 30, DiskBytes: 1 << 30, MaxContainers: 3, PortMin: 8000, PortMax: 8001,
	}}
	at.setAllowance(t, at.allow)
	labels := map[string]string{ownerLabel: ot.bobKey}
	ot.fake.Containers = append(ot.fake.Containers, container.Summary{
		ID: "idle", Names: []string{"/idle"}, Image: "nginx:1", ImageID: "sha256:plain", State: container.StateExited, Labels: labels,
	})
	ot.fake.ContainerDetails["idle"] = container.InspectResponse{
		ID: "idle", Name: "/idle", Image: "sha256:plain", State: &container.State{Status: container.StateExited},
		Config:     &container.Config{Image: "nginx:1", Labels: labels},
		HostConfig: &container.HostConfig{Resources: container.Resources{NanoCPUs: 250_000_000, Memory: 256 * mib}},
	}
	for i, c := range ot.fake.Containers {
		if c.ID == "alices" {
			ot.fake.Containers[i].Ports = []container.PortSummary{{PublicPort: 8000, PrivatePort: 80, Type: "tcp"}}
		}
	}
	at.s = &server{log: slog.New(slog.DiscardHandler), auth: ot.svc, allowances: ot.allowances}
	return at
}

func (at allowanceTest) setAllowance(t *testing.T, a allowances.Allowance) {
	t.Helper()
	if _, err := at.allowances.Set(context.Background(), at.bobID, at.local, a); err != nil {
		t.Fatal(err)
	}
}

// limits is a container asking for cpus (thousandths) and memory, with host ports bound
// to its port 80.
func limits(cpus, memory int64, ports ...string) (*container.Config, *container.HostConfig) {
	hc := &container.HostConfig{Resources: container.Resources{NanoCPUs: cpus * 1_000_000, Memory: memory}}
	if len(ports) > 0 {
		p, _ := network.ParsePort("80/tcp")
		hc.PortBindings = network.PortMap{}
		for _, hp := range ports {
			hc.PortBindings[p] = append(hc.PortBindings[p], network.PortBinding{HostPort: hp})
		}
	}
	return &container.Config{Image: "nginx:1"}, hc
}

// Each case checks a new container (or "mine" replaced) against bob's allowance, with
// "mine" (500m, 256 MiB) and the stopped "idle" (250m, 256 MiB) counted.
func TestFitAllowance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cpus     int64
		memory   int64
		ports    []string
		skip     string
		max      int64
		wantErr  string
		wantPort string
	}{
		{name: "fits what is left", cpus: 250, memory: 512 * mib},
		{name: "over by memory", cpus: 250, memory: 512*mib + 1, wantErr: "memory asked"},
		{name: "over by cpu", cpus: 251, memory: 256 * mib, wantErr: "CPUs asked"},
		{name: "over by container count", cpus: 100, memory: mib, max: 2, wantErr: "2 containers and you have 2"},
		{name: "a stopped container still counts", cpus: 500, memory: mib, wantErr: "0.25 left"},
		{name: "port outside the range", cpus: 100, memory: mib, ports: []string{"9000"}, wantErr: "host port 9000 is outside"},
		{name: "port range partly outside", cpus: 100, memory: mib, ports: []string{"8001-8002"}, wantErr: "host port 8002"},
		{name: "port 0 picks a free one in the range", cpus: 100, memory: mib, ports: []string{""}, wantPort: "8001"},
		{name: "no free port left", cpus: 100, memory: mib, ports: []string{"", ""}, wantErr: "no free host port"},
		{name: "recreate with a bigger limit counts the difference", cpus: 750, memory: 768 * mib, skip: "mine"},
		{name: "recreate over by the difference", cpus: 751, memory: 256 * mib, skip: "mine", wantErr: "CPUs asked"},
	}
	at := newAllowanceTest(t)
	for _, tt := range tests {
		// One allowance for all: in sequence.
		t.Run(tt.name, func(t *testing.T) {
			a := at.allow
			if tt.max != 0 {
				a.MaxContainers = tt.max
			}
			at.setAllowance(t, a)
			cfg, hc := limits(tt.cpus, tt.memory, tt.ports...)
			unlock, err := at.s.fitAllowance(t.Context(), at.local, at.fake, at.bobKey, cfg, hc, tt.skip, true)
			if tt.wantErr != "" {
				var se *statusError
				if !errors.As(err, &se) || se.code != http.StatusConflict || !strings.Contains(se.msg, tt.wantErr) {
					t.Fatalf("fitAllowance = %v, want 409 %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fitAllowance = %v", err)
			}
			unlock()
			if tt.wantPort != "" {
				for _, bs := range hc.PortBindings {
					if bs[0].HostPort != tt.wantPort {
						t.Errorf("host port = %q, want %s (8000 is alice's)", bs[0].HostPort, tt.wantPort)
					}
				}
			}
		})
	}

	// An admin's container (no owner) has no allowance; a user without one gets nothing.
	cfg, hc := limits(0, 0)
	if _, err := at.s.fitAllowance(t.Context(), at.local, at.fake, "", cfg, hc, "", true); err != nil {
		t.Errorf("admin's container = %v", err)
	}
	if _, err := at.s.fitAllowance(t.Context(), at.other, at.fake, at.bobKey, cfg, hc, "", true); err == nil || !strings.Contains(err.Error(), "no allowance") {
		t.Errorf("without an allowance = %v", err)
	}
	if _, err := at.s.fitAllowance(t.Context(), at.local, at.fake, at.bobKey, cfg, hc, "", true); err == nil || !strings.Contains(err.Error(), "CPU limit") {
		t.Errorf("without limits = %v", err)
	}
	// An image that sets the owner label would make the container the admins' and escape
	// every later check.
	cfg, hc = limits(100, mib)
	cfg.Image = "sha256:forged"
	if _, err := at.s.fitAllowance(t.Context(), at.local, at.fake, at.bobKey, cfg, hc, "", true); err == nil || !strings.Contains(err.Error(), "reserved for nudge") {
		t.Errorf("image with the owner label = %v", err)
	}
}

// Two creates that each fit what is left, but not together: only one passes.
func TestFitAllowanceParallel(t *testing.T) {
	t.Parallel()
	at := newAllowanceTest(t)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		passed int
	)
	for i := range 2 {
		wg.Go(func() {
			cfg, hc := limits(250, 256*mib)
			unlock, err := at.s.fitAllowance(context.Background(), at.local, at.fake, at.bobKey, cfg, hc, "", true)
			if err != nil {
				return
			}
			// The create, while the lock is held: the other check must see it.
			id := fmt.Sprintf("new%d", i)
			at.fake.Containers = append(at.fake.Containers, container.Summary{ID: id, ImageID: "sha256:plain", Labels: map[string]string{ownerLabel: at.bobKey}})
			at.fake.ContainerDetails[id] = container.InspectResponse{ID: id, HostConfig: hc}
			mu.Lock()
			passed++
			mu.Unlock()
			unlock()
		})
	}
	wg.Wait()
	if passed != 1 {
		t.Errorf("%d creates passed, want 1", passed)
	}
}

// A user's start is checked: it works while the container fits and is refused once an
// admin lowers the allowance below what bob's containers take.
func TestStartChecksAllowance(t *testing.T) {
	t.Parallel()
	at := newAllowanceTest(t)
	start := func() int {
		return do(t, at.h, request{method: "POST", path: at.path(at.local, "/containers/idle/start"), cookie: at.bob}).Code
	}
	if code := start(); code != http.StatusNoContent {
		t.Fatalf("start = %d", code)
	}
	low := at.allow
	low.CPUsMilli = 600
	at.setAllowance(t, low)
	rec := do(t, at.h, request{method: "POST", path: at.path(at.local, "/containers/idle/restart"), cookie: at.bob})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "over your allowance") {
		t.Errorf("restart over the allowance = %d %s", rec.Code, rec.Body)
	}
	if code := start(); code != http.StatusConflict {
		t.Errorf("start over the allowance = %d", code)
	}
	// The admins' container isn't checked.
	if rec := do(t, at.h, request{method: "POST", path: at.path(at.local, "/containers/admins/start"), cookie: at.admin}); rec.Code != http.StatusNoContent {
		t.Errorf("admin's start = %d %s", rec.Code, rec.Body)
	}
}

func TestAllowanceEndpoints(t *testing.T) {
	t.Parallel()
	at := newAllowanceTest(t)
	q := fmt.Sprintf("/allowance?user=%d", at.bobID)
	body := `{"cpus_milli":2000,"memory_bytes":2147483648,"disk_bytes":1073741824,"max_containers":4,"port_min":8000,"port_max":8009,"images":["nginx"]}`
	if rec := do(t, at.h, request{method: "PUT", path: at.path(at.local, q), cookie: at.admin, body: body}); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	for _, tt := range []struct {
		name, method, path, body string
		admin                    bool
		code                     int
		want                     string
	}{
		{"invalid", "PUT", q, `{"port_min":9000,"port_max":8000}`, true, http.StatusBadRequest, `"port_max"`},
		{"admin without a user", "GET", "/allowance", "", true, http.StatusBadRequest, "user must be"},
		{"admin's own", "GET", "/allowance?user=1", "", true, http.StatusBadRequest, "admins have no allowance"},
		{"missing user", "GET", "/allowance?user=99", "", true, http.StatusNotFound, ""},
		{"user sets their own", "PUT", "/allowance", body, false, http.StatusForbidden, ""},
		{"user reads another's", "GET", "/allowance?user=1", "", false, http.StatusForbidden, ""},
	} {
		cookie := at.bob
		if tt.admin {
			cookie = at.admin
		}
		rec := do(t, at.h, request{method: tt.method, path: at.path(at.local, tt.path), cookie: cookie, body: tt.body})
		if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s = %d %s, want %d %s", tt.name, rec.Code, rec.Body, tt.code, tt.want)
		}
	}

	// bob's own, with "mine" and the stopped "idle" counted.
	for i, c := range at.fake.Containers {
		if c.ID == "mine" {
			at.fake.Containers[i].SizeRw = 5 * mib
		}
	}
	rec := do(t, at.h, request{method: "GET", path: at.path(at.local, "/allowance"), cookie: at.bob})
	var got allowanceResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	if got.User != at.bobID || got.Limits == nil || got.Limits.CPUsMilli != 2000 || len(got.Limits.Images) != 1 {
		t.Errorf("limits = %+v", got.Limits)
	}
	want := allowanceUsageJSON{CPUsMilli: 750, MemoryBytes: 512 * mib, DiskBytes: 5 * mib, Containers: 2}
	if got.Used != want {
		t.Errorf("used = %+v, want %+v", got.Used, want)
	}
	if got.Left == nil || *got.Left != (allowanceUsageJSON{CPUsMilli: 1250, MemoryBytes: 1536 * mib, DiskBytes: 1019 * mib, Containers: 2}) {
		t.Errorf("left = %+v", got.Left)
	}

	if rec := do(t, at.h, request{method: "DELETE", path: at.path(at.local, q), cookie: at.admin}); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, at.h, request{method: "DELETE", path: at.path(at.local, q), cookie: at.admin}); rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d", rec.Code)
	}
	rec = do(t, at.h, request{method: "GET", path: at.path(at.local, "/allowance"), cookie: at.bob})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"limits":null`) {
		t.Errorf("GET without an allowance = %d %s", rec.Code, rec.Body)
	}

	var actions []string
	for _, e := range auditEntries(t, at.h, at.admin, "").Entries {
		if strings.Contains(e.Action, "allowance") && e.Target == "bob" {
			actions = append(actions, fmt.Sprintf("%s %d", e.Action, e.Status))
		}
	}
	if len(actions) < 3 {
		t.Errorf("audit = %v, want the PUT, the invalid PUT and the DELETE", actions)
	}
}

// A user's update pulls for them, and the pull counts toward their disk: one the
// allowance has no room for is refused before it starts, one that takes them over is
// removed again, and the image an update replaces is removed if their pull brought it.
func TestUpdatePullsWithinAllowance(t *testing.T) {
	t.Parallel()
	at := newAllowanceTest(t)
	pulled := func(id string, size int64) {
		at.fake.FailAction = func(a dockertest.Action) error {
			if a.Name == "image-pull" {
				img := image.InspectResponse{ID: id, Size: size, Config: &dockerspec.DockerOCIImageConfig{}}
				at.fake.ImageDetails["nginx:1"], at.fake.ImageDetails[id] = img, img
			}
			return nil
		}
	}
	update := func() *httptest.ResponseRecorder {
		return do(t, at.h, request{method: "POST", path: at.path(at.local, "/containers/mine/update"), cookie: at.bob})
	}
	removed := func() (ids []string) {
		for _, a := range at.fake.Actions() {
			if a.Name == "image-remove" {
				ids = append(ids, a.ID)
			}
		}
		return ids
	}
	pulls := func() []string {
		ids, err := at.allowances.Pulls(t.Context(), at.bobID, at.local)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}

	pulled("sha256:small", 100*mib)
	if rec := update(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"updated":true`) {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	if got := pulls(); !slices.Equal(got, []string{"sha256:small"}) {
		t.Errorf("pulls = %v, want the image recorded", got)
	}

	pulled("sha256:big", 1<<30)
	if rec := update(); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "over your allowance") {
		t.Fatalf("update over = %d %s", rec.Code, rec.Body)
	}
	if got := removed(); !slices.Equal(got, []string{"sha256:big"}) {
		t.Errorf("removed = %v, want the big image", got)
	}
	if got := pulls(); !slices.Equal(got, []string{"sha256:small"}) {
		t.Errorf("pulls = %v, want the big one forgotten", got)
	}

	// An image already on the host (another user's or an admin's) isn't counted or removed.
	at.fake.Images = []image.Summary{{ID: "sha256:shared"}}
	pulled("sha256:shared", 1<<30)
	if rec := update(); rec.Code != http.StatusOK {
		t.Fatalf("update to a present image = %d %s", rec.Code, rec.Body)
	}
	if got := removed(); !slices.Equal(got, []string{"sha256:big"}) {
		t.Errorf("removed = %v, want the present image kept", got)
	}
	if got := pulls(); !slices.Equal(got, []string{"sha256:small"}) {
		t.Errorf("pulls = %v, want the present image not counted", got)
	}

	// The container runs the small one now; the next update removes it.
	mine := at.fake.ContainerDetails["mine"]
	mine.Image = "sha256:small"
	at.fake.ContainerDetails["mine"] = mine
	pulled("sha256:next", 10*mib)
	if rec := update(); rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	if got := removed(); !slices.Equal(got, []string{"sha256:big", "sha256:small"}) {
		t.Errorf("removed = %v, want the replaced image too", got)
	}
	if got := pulls(); !slices.Equal(got, []string{"sha256:next"}) {
		t.Errorf("pulls = %v", got)
	}

	// Used up: refused before the pull.
	full := at.allow
	full.DiskBytes = 10 * mib
	at.setAllowance(t, full)
	before := len(at.fake.Actions())
	if rec := update(); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "disk") {
		t.Fatalf("update when full = %d %s", rec.Code, rec.Body)
	}
	if after := at.fake.Actions()[before:]; len(after) != 0 {
		t.Errorf("actions = %+v, want none", after)
	}
}
