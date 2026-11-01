package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	godigest "github.com/opencontainers/go-digest"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/selfupdate"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

const (
	selfTestID   = "5e1f0123456789ab"
	selfTestHost = "5e1f01234567"
	selfTestRef  = "registry.example/nudge:latest"
)

// selfFake is a daemon running nudge's container (selfTestID, compose project "nudge")
// on image sha256:old, with sha256:new under the tag once pulled.
func selfFake() *dockertest.Fake {
	return &dockertest.Fake{Resources: dockertest.Resources{
		Containers: []container.Summary{{ID: selfTestID, Names: []string{"/nudge"}, Image: selfTestRef, ImageID: "sha256:old", State: container.StateRunning}},
		ContainerDetails: map[string]container.InspectResponse{selfTestID: {
			ID: selfTestID, Name: "/nudge", Image: "sha256:old",
			Config:     &container.Config{Hostname: selfTestHost, Image: selfTestRef, Labels: map[string]string{stacks.ProjectLabel: "nudge"}},
			HostConfig: &container.HostConfig{},
			State:      &container.State{Running: true, Status: container.StateRunning},
			Mounts:     []container.MountPoint{{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"}},
		}},
		ImageDetails: map[string]image.InspectResponse{
			selfTestRef:  {ID: "sha256:new"},
			"sha256:old": {ID: "sha256:old", RepoDigests: []string{"registry.example/nudge@sha256:aaa"}},
		},
		Distribution: map[string]godigest.Digest{selfTestRef: "sha256:bbb"},
	}}
}

type selfTest struct {
	envTest
	local, agent *dockertest.Fake
}

// newSelfTest runs the API with nudge's container on local's daemon and the agent's on
// agent's (environment "nas"); agentErr fails the agent's answer.
func newSelfTest(t *testing.T, agentErr error) selfTest {
	t.Helper()
	st := selfTest{local: selfFake(), agent: selfFake()}
	h, svc, db := newTestHandlerOpts(t, func(env environments.Environment) (docker.Client, error) {
		if env.Kind == environments.KindAgent {
			return st.agent, nil
		}
		return st.local, nil
	}, func(o *Options) {
		o.Self = stacks.Self{ID: selfTestID, Hostname: selfTestHost}
		o.DockerHost = "unix:///var/run/docker.sock"
		o.AgentSelf = func(context.Context, environments.Environment) (stacks.Self, string, error) {
			return stacks.Self{ID: selfTestID, Hostname: selfTestHost}, "unix:///var/run/docker.sock", agentErr
		}
	})
	ctx := context.Background()
	if _, err := svc.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds})
	st.envTest = envTest{h: h, cookie: sessionCookieOf(t, rec), ids: map[string]int64{}}
	for _, e := range []struct{ name, kind, version string }{{"local", "local", ""}, {"nas", "agent", "v0.9.0"}} {
		res, err := db.ExecContext(ctx, `INSERT INTO environments (name, kind, created_at, agent_version) VALUES (?, ?, 0, NULLIF(?, ''))`, e.name, e.kind, e.version)
		if err != nil {
			t.Fatal(err)
		}
		if st.ids[e.name], err = res.LastInsertId(); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func (st selfTest) do(t *testing.T, method, path string) (int, string) {
	t.Helper()
	path = strings.ReplaceAll(path, "%d", strconv.FormatInt(st.ids["nas"], 10))
	rec := do(t, st.h, request{method: method, path: path, cookie: st.cookie})
	return rec.Code, rec.Body.String()
}

func TestSelfUpdateStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path string
		setup      func(local *dockertest.Fake)
		want       selfUpdateJSON
	}{
		{
			name: "server", path: "/api/v1/system/update",
			want: selfUpdateJSON{Version: "v1.0.0", Image: selfTestRef, Status: "unknown", Error: "not checked yet", CanUpdate: true},
		},
		{
			name: "server checked", path: "/api/v1/system/update/check",
			want: selfUpdateJSON{Version: "v1.0.0", Image: selfTestRef, Status: "available", LocalDigest: "sha256:aaa", RemoteDigest: "sha256:bbb", CanUpdate: true},
		},
		{
			// The tag names a newer image, so the daemon lists the running one by ID.
			name: "server tag moved", path: "/api/v1/system/update/check",
			setup: func(f *dockertest.Fake) { f.Containers[0].Image = "0123456789ab" },
			want:  selfUpdateJSON{Version: "v1.0.0", Image: selfTestRef, Status: "available", LocalDigest: "sha256:aaa", RemoteDigest: "sha256:bbb", CanUpdate: true},
		},
		{
			name: "server not covered", path: "/api/v1/system/update/check",
			setup: func(f *dockertest.Fake) { f.Containers[0].ImageID = "" },
			want:  selfUpdateJSON{Version: "v1.0.0", Image: selfTestRef, Status: "unknown", Error: "the last check didn't cover this image", CanUpdate: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := newSelfTest(t, nil)
			if tt.setup != nil {
				tt.setup(st.local)
			}
			method := "GET"
			if strings.HasSuffix(tt.path, "/check") {
				method = "POST"
			}
			code, body := st.do(t, method, tt.path)
			if code != http.StatusOK {
				t.Fatalf("%s = %d %s", tt.path, code, body)
			}
			var got selfUpdateJSON
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}
			got.CheckedAt = nil
			if got != tt.want {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestSelfUpdateStart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path string
		// current leaves the tag on the running image: the pull brought nothing newer.
		current bool
		helper  bool
		code    int
		want    string
	}{
		{name: "server", path: "/api/v1/system/update", code: http.StatusAccepted, want: `{"started":true}`},
		{name: "nothing newer", path: "/api/v1/system/update", current: true, code: http.StatusOK, want: `{"started":false}`},
		{name: "already running", path: "/api/v1/system/update", helper: true, code: http.StatusConflict, want: `{"error":"an update of nudge is already running"}`},
		{name: "local is no agent", path: "/api/v1/environments/1/agent/update", code: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := newSelfTest(t, nil)
			fake := st.local
			if tt.path != "/api/v1/system/update" {
				fake = st.agent
			}
			if tt.current {
				fake.ImageDetails[selfTestRef] = image.InspectResponse{ID: "sha256:old"}
			}
			if tt.helper {
				fake.Containers = append(fake.Containers, container.Summary{ID: "helper", State: container.StateRunning, Labels: map[string]string{selfupdate.Label: selfTestID}})
			}
			code, body := st.do(t, "POST", tt.path)
			if code != tt.code || (tt.want != "" && body != tt.want+"\n") {
				t.Fatalf("POST %s = %d %s, want %d %s", tt.path, code, body, tt.code, tt.want)
			}
			var names []string
			for _, a := range fake.Actions() {
				names = append(names, a.Name)
			}
			want := "[]"
			switch {
			case code == http.StatusAccepted:
				want = "[image-pull create start]"
			case code == http.StatusOK || tt.helper:
				want = "[image-pull]"
			}
			if got := fmt.Sprint(names); got != want {
				t.Errorf("actions = %s, want %s", got, want)
			}
		})
	}
}

func TestSelfGuards(t *testing.T) {
	t.Parallel()
	st := newSelfTest(t, nil)
	code, body := st.do(t, "POST", "/api/v1/environments/1/containers/"+selfTestID+"/update")
	if code != http.StatusConflict || body != fmt.Sprintf("{\"error\":%q}\n", errSelfContainer.msg) {
		t.Errorf("update own container = %d %s, want 409", code, body)
	}
}

// TestFindSelfAgent covers the agent side of the update endpoints and guards, which find
// the agent's container from its answer; the rest is the server's path. (Agent
// environments are reached through a real tunnel, so the HTTP tests use the server's.)
func TestFindSelfAgent(t *testing.T) {
	t.Parallel()
	agent := environments.Environment{ID: 2, Name: "nas", Kind: environments.KindAgent}
	answer := func(err error) func(context.Context, environments.Environment) (stacks.Self, string, error) {
		return func(context.Context, environments.Environment) (stacks.Self, string, error) {
			return stacks.Self{ID: selfTestID, Hostname: selfTestHost}, "unix:///var/run/docker.sock", err
		}
	}
	tests := []struct {
		name      string
		agentSelf func(context.Context, environments.Environment) (stacks.Self, string, error)
		want      error
		wantID    string
	}{
		{name: "found", agentSelf: answer(nil), wantID: selfTestID},
		{name: "old agent", agentSelf: answer(selfupdate.ErrOldAgent), want: selfupdate.ErrOldAgent},
		{name: "not wired", want: selfupdate.ErrNoSelf},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &server{agentSelf: tt.agentSelf}
			got, err := s.findSelf(t.Context(), agent, selfFake())
			if !errors.Is(err, tt.want) || got.ID != tt.wantID {
				t.Fatalf("findSelf = %+v, %v; want %s, %v", got, err, tt.wantID, tt.want)
			}
			// The guard blocks the agent's own container only when it is found.
			if id, project := s.selfContainer(t.Context(), agent, selfFake()); id != tt.wantID || (id != "" && project != "nudge") {
				t.Errorf("selfContainer = %q, %q", id, project)
			}
		})
	}
}

func TestSelfStackGuard(t *testing.T) {
	t.Parallel()
	st := newStackTest(t, func(o *Options) {
		o.Self = stacks.Self{ID: selfTestID, Hostname: selfTestHost}
		o.DockerHost = "unix:///var/run/docker.sock"
	})
	st.fake.ContainerDetails = map[string]container.InspectResponse{selfTestID: selfFake().ContainerDetails[selfTestID]}
	for _, name := range []string{"nudge", "web"} {
		if code, body := st.do(t, "POST", "local", "", `{"name":"`+name+`","compose":"services: {}\n"}`); code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", name, code, body)
		}
	}
	if code, body := st.do(t, "POST", "local", "/nudge/up", ""); code != http.StatusConflict {
		t.Errorf("up nudge's own stack = %d %s, want 409", code, body)
	}
	if code, body := st.do(t, "POST", "local", "/web/up", ""); code != http.StatusOK {
		t.Errorf("up another stack = %d %s, want 200", code, body)
	}
}
