package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestContainerActions(t *testing.T) {
	t.Parallel()
	five := 5
	tests := []struct {
		name     string
		method   string
		path     string
		fake     *dockertest.Fake
		wantCode int
		wantErr  string
		want     []dockertest.Action
	}{
		{name: "start", method: "POST", path: "/containers/web/start", wantCode: 204,
			want: []dockertest.Action{{Name: "start", ID: "web", Options: client.ContainerStartOptions{}}}},
		{name: "stop", method: "POST", path: "/containers/web/stop", wantCode: 204,
			want: []dockertest.Action{{Name: "stop", ID: "web", Options: client.ContainerStopOptions{}}}},
		{name: "stop with timeout", method: "POST", path: "/containers/web/stop?timeout=5", wantCode: 204,
			want: []dockertest.Action{{Name: "stop", ID: "web", Options: client.ContainerStopOptions{Timeout: &five}}}},
		{name: "restart with timeout", method: "POST", path: "/containers/web/restart?timeout=5", wantCode: 204,
			want: []dockertest.Action{{Name: "restart", ID: "web", Options: client.ContainerRestartOptions{Timeout: &five}}}},
		{name: "kill", method: "POST", path: "/containers/web/kill", wantCode: 204,
			want: []dockertest.Action{{Name: "kill", ID: "web", Options: client.ContainerKillOptions{}}}},
		{name: "kill with signal", method: "POST", path: "/containers/web/kill?signal=SIGHUP", wantCode: 204,
			want: []dockertest.Action{{Name: "kill", ID: "web", Options: client.ContainerKillOptions{Signal: "SIGHUP"}}}},
		{name: "remove", method: "DELETE", path: "/containers/web", wantCode: 204,
			want: []dockertest.Action{{Name: "remove", ID: "web", Options: client.ContainerRemoveOptions{}}}},
		{name: "remove with force and volumes", method: "DELETE", path: "/containers/web?force=true&volumes=1", wantCode: 204,
			want: []dockertest.Action{{Name: "remove", ID: "web", Options: client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}}}},

		{name: "unknown container", method: "POST", path: "/containers/nope/start", wantCode: 404, wantErr: "container not found"},
		{name: "invalid container id", method: "DELETE", path: "/containers/..%2Fimages", wantCode: 404, wantErr: "container not found"},
		{name: "negative timeout", method: "POST", path: "/containers/web/stop?timeout=-1", wantCode: 400, wantErr: "timeout must be 0 to 600 seconds"},
		{name: "timeout too long", method: "POST", path: "/containers/web/restart?timeout=601", wantCode: 400},
		{name: "timeout not a number", method: "POST", path: "/containers/web/stop?timeout=soon", wantCode: 400},
		{name: "invalid signal", method: "POST", path: "/containers/web/kill?signal=KILL%3Brm", wantCode: 400},
		{name: "invalid force", method: "DELETE", path: "/containers/web?force=yes", wantCode: 400, wantErr: "force must be true or false"},
		{name: "invalid volumes", method: "DELETE", path: "/containers/web?volumes=x", wantCode: 400, wantErr: "volumes must be true or false"},
		{name: "remove running", method: "DELETE", path: "/containers/web",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: fmt.Errorf("secret-detail: %w", cerrdefs.ErrConflict)}},
			wantCode: 409, wantErr: "the container is running: stop it first, or remove it with force=true"},
		{name: "kill stopped", method: "POST", path: "/containers/web/kill",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: cerrdefs.ErrConflict}},
			wantCode: 409, wantErr: "the container cannot be killed in its current state (is it running?)"},
		{name: "unknown signal", method: "POST", path: "/containers/web/kill?signal=FOO",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: fmt.Errorf("secret-detail: %w", cerrdefs.ErrInvalidArgument)}},
			wantCode: 400, wantErr: `unknown signal "FOO"`},
		{name: "daemon error", method: "POST", path: "/containers/web/start",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: errors.New("Error response from daemon: secret-detail")}},
			wantCode: 502},
		{name: "offline", method: "POST", path: "/containers/web/stop",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}},
			wantCode: 503, wantErr: "the environment is offline"},
		{name: "wrong method", method: "GET", path: "/containers/web/start", wantCode: 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := tt.fake
			if fake == nil {
				fake = &dockertest.Fake{}
			}
			fake.ContainerDetails = map[string]container.InspectResponse{"web": {ID: "abc123", Name: "/web"}}
			et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
			path := fmt.Sprintf("/api/v1/environments/%d%s", et.ids["local"], tt.path)
			rec := do(t, et.h, request{method: tt.method, path: path, cookie: et.cookie})
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			if tt.wantCode == http.StatusNoContent {
				if rec.Body.Len() != 0 {
					t.Errorf("body = %q, want none", rec.Body)
				}
			} else {
				var body struct{ Error string }
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
					t.Fatalf("body = %s, want a JSON error", rec.Body)
				}
				if tt.wantErr != "" && body.Error != tt.wantErr {
					t.Errorf("error = %q, want %q", body.Error, tt.wantErr)
				}
				if strings.Contains(body.Error, "secret") {
					t.Errorf("error leaks daemon details: %s", rec.Body)
				}
			}
			if got := fake.Actions(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("actions = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestContainerActionsNeedSession(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{Resources: dockertest.Resources{ContainerDetails: map[string]container.InspectResponse{"web": {}}}}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
	path := fmt.Sprintf("/api/v1/environments/%d/containers/web", et.ids["local"])
	for _, req := range []request{{method: "POST", path: path + "/stop"}, {method: "DELETE", path: path}} {
		if rec := do(t, et.h, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session: code = %d, want 401", req.method, req.path, rec.Code)
		}
	}
	if got := fake.Actions(); len(got) != 0 {
		t.Errorf("actions = %+v, want none", got)
	}
}
