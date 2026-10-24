package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

// envTest is an API with one environment per fake, named after its key, and a session.
type envTest struct {
	h      http.Handler
	cookie *http.Cookie
	ids    map[string]int64
}

func newEnvTest(t *testing.T, fakes map[string]*dockertest.Fake) envTest {
	t.Helper()
	h, svc, db := newTestHandlerWith(t, func(env environments.Environment) (docker.Client, error) {
		f, ok := fakes[env.Name]
		if !ok {
			return nil, fmt.Errorf("no fake for %q", env.Name)
		}
		return f, nil
	})
	ctx := context.Background()
	if _, err := svc.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds})
	et := envTest{h: h, cookie: sessionCookieOf(t, rec), ids: map[string]int64{}}
	for _, name := range []string{"local", "down", "slow", "broken"} {
		if _, ok := fakes[name]; !ok {
			continue
		}
		res, err := db.ExecContext(ctx, `INSERT INTO environments (name, kind, created_at) VALUES (?, 'local', 0)`, name)
		if err != nil {
			t.Fatal(err)
		}
		if et.ids[name], err = res.LastInsertId(); err != nil {
			t.Fatal(err)
		}
	}
	return et
}

func TestListEnvironments(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{
		"local": {},
		"down":  {PingErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")}},
	})
	rec := do(t, et.h, request{method: "GET", path: "/api/v1/environments", cookie: et.cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body)
	}
	var got []environmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d environments, want 2: %s", len(got), rec.Body)
	}
	online := map[string]bool{}
	for _, env := range got {
		online[env.Name] = env.Online
		if env.Kind != "local" {
			t.Errorf("%s kind = %q", env.Name, env.Kind)
		}
	}
	if !online["local"] || online["down"] {
		t.Errorf("online = %v, want local up and down offline", online)
	}

	if rec := do(t, et.h, request{method: "GET", path: "/api/v1/environments"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("without session: code = %d, want 401", rec.Code)
	}
}

func TestGetEnvironment(t *testing.T) {
	t.Parallel()
	info := system.Info{
		Name: "host1", OperatingSystem: "Debian GNU/Linux 13", OSType: "linux", KernelVersion: "6.8.0",
		Architecture: "x86_64", NCPU: 8, MemTotal: 16 << 30, Containers: 5, ContainersRunning: 3,
		ContainersStopped: 2, Images: 7, Driver: "overlay2", DockerRootDir: "/var/lib/docker",
		HTTPProxy: "http://user:secret@proxy:3128",
	}
	et := newEnvTest(t, map[string]*dockertest.Fake{
		"local": {
			InfoResult:    client.SystemInfoResult{Info: info},
			VersionResult: client.ServerVersionResult{Version: "28.5.1", APIVersion: "1.51", MinAPIVersion: "1.24"},
		},
		"down":   {InfoErr: &net.OpError{Op: "dial", Err: errors.New("connection refused: secret-detail")}},
		"slow":   {InfoErr: context.DeadlineExceeded},
		"broken": {VersionErr: errors.New("Error response from daemon: secret-detail")},
	})

	rec := do(t, et.h, request{method: "GET", path: fmt.Sprintf("/api/v1/environments/%d", et.ids["local"]), cookie: et.cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body)
	}
	var got environmentDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := dockerResponse{
		Name: "host1", Version: "28.5.1", APIVersion: "1.51", MinAPIVersion: "1.24", OS: "Debian GNU/Linux 13",
		OSType: "linux", KernelVersion: "6.8.0", Architecture: "x86_64", NCPU: 8, MemTotal: 16 << 30,
		Containers: 5, ContainersRunning: 3, ContainersStopped: 2, Images: 7, StorageDriver: "overlay2",
		DockerRootDir: "/var/lib/docker",
	}
	if got.Docker != want || !got.Online || got.Name != "local" || got.LastSeenAt == nil {
		t.Errorf("got %+v\nwant docker %+v", got, want)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("body leaks the proxy credentials: %s", rec.Body)
	}

	tests := []struct {
		name     string
		path     string
		wantCode int
	}{
		{name: "unknown id", path: "/api/v1/environments/999", wantCode: 404},
		{name: "not a number", path: "/api/v1/environments/local", wantCode: 404},
		{name: "unreachable", path: fmt.Sprintf("/api/v1/environments/%d", et.ids["down"]), wantCode: 503},
		{name: "timeout", path: fmt.Sprintf("/api/v1/environments/%d", et.ids["slow"]), wantCode: 504},
		{name: "daemon error", path: fmt.Sprintf("/api/v1/environments/%d", et.ids["broken"]), wantCode: 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := do(t, et.h, request{method: "GET", path: tt.path, cookie: et.cookie})
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "secret") || !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("body = %s, want a JSON error without daemon details", rec.Body)
			}
		})
	}
}
