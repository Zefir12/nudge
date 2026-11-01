package selfupdate

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

const (
	selfID   = "c0ffee0123456789"
	hostname = "c0ffee012345"
	ref      = "registry.example/nudge:latest"
)

// self is nudge's container as the daemon shows it, socket bind-mounted.
func self() container.InspectResponse {
	return container.InspectResponse{
		ID: selfID, Name: "/nudge", Image: "sha256:old",
		Config: &container.Config{
			Hostname: hostname, Image: ref,
			Labels: map[string]string{stacks.ProjectLabel: "nudge"},
		},
		HostConfig: &container.HostConfig{},
		State:      &container.State{Running: true, Status: container.StateRunning},
		Mounts: []container.MountPoint{
			{Type: "volume", Name: "nudge-data", Destination: "/data", RW: true},
			{Type: "bind", Source: "/run/docker.sock", Destination: "/var/run/docker.sock"},
		},
	}
}

func newFake(c container.InspectResponse) *dockertest.Fake {
	return &dockertest.Fake{Resources: dockertest.Resources{
		ContainerDetails: map[string]container.InspectResponse{c.ID: c},
		ImageDetails: map[string]image.InspectResponse{
			ref:          {ID: "sha256:new"},
			"sha256:old": {ID: "sha256:old"},
		},
	}}
}

func TestFind(t *testing.T) {
	t.Parallel()
	noSocket := self()
	noSocket.Mounts = noSocket.Mounts[:1]
	tests := []struct {
		name       string
		self       stacks.Self
		dockerHost string
		want       error
		wantSocket string
	}{
		{name: "found", self: stacks.Self{ID: selfID, Hostname: hostname}, dockerHost: "unix:///var/run/docker.sock", wantSocket: "/run/docker.sock"},
		{name: "outside a container", dockerHost: "unix:///var/run/docker.sock", want: ErrNoSelf},
		{name: "unknown to the daemon", self: stacks.Self{ID: "other", Hostname: "other"}, dockerHost: "unix:///var/run/docker.sock", want: ErrNoSelf},
		{name: "another hostname", self: stacks.Self{ID: selfID, Hostname: "someone"}, dockerHost: "unix:///var/run/docker.sock", want: ErrNoSelf},
		{name: "tcp daemon", self: stacks.Self{ID: selfID, Hostname: hostname}, dockerHost: "tcp://docker:2375", want: ErrNoSocket},
		{name: "socket elsewhere", self: stacks.Self{ID: selfID, Hostname: hostname}, dockerHost: "unix:///docker.sock", want: ErrNoSocket},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Find(t.Context(), newFake(self()), tt.self, tt.dockerHost)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Find = %v, want %v", err, tt.want)
			}
			if got.socket != tt.wantSocket {
				t.Errorf("socket = %q, want %q", got.socket, tt.wantSocket)
			}
			if tt.want == nil && (got.ID != selfID || got.Name != "nudge" || got.Image != ref || got.ImageID != "sha256:old" || got.Project != "nudge") {
				t.Errorf("Find = %+v", got)
			}
		})
	}
	_, err := Find(t.Context(), newFake(noSocket), stacks.Self{ID: selfID, Hostname: hostname}, "unix:///var/run/docker.sock")
	if !errors.Is(err, ErrNoSocket) {
		t.Errorf("Find without the socket mounted = %v, want ErrNoSocket", err)
	}
	removed := self()
	removed.HostConfig.AutoRemove = true
	_, err = Find(t.Context(), newFake(removed), stacks.Self{ID: selfID, Hostname: hostname}, "unix:///var/run/docker.sock")
	if !errors.Is(err, ErrAutoRemove) {
		t.Errorf("Find of a --rm container = %v, want ErrAutoRemove", err)
	}
}

func TestLaunch(t *testing.T) {
	t.Parallel()
	f := newFake(self())
	target, err := Find(t.Context(), f, stacks.Self{ID: selfID, Hostname: hostname}, "unix:///var/run/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	id, err := Launch(t.Context(), f, target)
	if err != nil || id != "new-1" {
		t.Fatalf("Launch = %q, %v", id, err)
	}
	acts := f.Actions()
	if len(acts) != 2 || acts[0].Name != "create" || acts[1].Name != "start" || acts[1].ID != "new-1" {
		t.Fatalf("actions = %+v, want create and start", acts)
	}
	opts := acts[0].Options.(client.ContainerCreateOptions)
	if opts.Config.Image != ref || !slices.Equal(opts.Config.Cmd, []string{"self-update", "-docker-host", "unix:///var/run/docker.sock", selfID}) {
		t.Errorf("helper config = %+v", opts.Config)
	}
	if opts.Config.Labels[Label] != selfID || !opts.HostConfig.AutoRemove ||
		!slices.Equal(opts.HostConfig.Binds, []string{"/run/docker.sock:/var/run/docker.sock"}) {
		t.Errorf("helper = labels %v, host config %+v", opts.Config.Labels, opts.HostConfig)
	}

	// A helper still at work refuses a second one.
	f.Containers = []container.Summary{{ID: "helper", State: container.StateRunning, Labels: map[string]string{Label: selfID}}}
	if _, err := Launch(t.Context(), f, target); !errors.Is(err, ErrRunning) {
		t.Errorf("second Launch = %v, want ErrRunning", err)
	}
	pinned := target
	pinned.Image = "registry.example/nudge@sha256:abc"
	if _, err := Launch(t.Context(), f, pinned); !errors.Is(err, ErrPinned) {
		t.Errorf("Launch of a pinned image = %v, want ErrPinned", err)
	}
	if _, err := Launch(t.Context(), f, Target{ID: selfID, Image: ref}); !errors.Is(err, ErrNoSocket) {
		t.Errorf("Launch without a socket = %v, want ErrNoSocket", err)
	}
}

// instant fires every wait at once except the swap's limit, which never fires.
func instant(limit time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		if d == limit {
			return nil
		}
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
}

func TestSwap(t *testing.T) {
	t.Parallel()
	running := container.State{Running: true, Status: container.StateRunning}
	tests := []struct {
		name string
		// newState is how the new container ("new-1") inspects once started.
		newState container.State
		failOn   string
		old      func(*container.InspectResponse)
		// aside is a container an earlier update left as nudge-nudge-old.
		aside *container.State
		want  error
		// fails is any error other than want.
		fails bool
		// actions are the recorded action names and IDs.
		actions []string
	}{
		{
			name: "replaced", newState: running,
			actions: []string{"stop " + selfID, "rename " + selfID, "create nudge", "start new-1", "remove " + selfID},
		},
		{
			name:     "healthy",
			newState: container.State{Running: true, Status: container.StateRunning, Health: &container.Health{Status: container.Healthy}},
			actions:  []string{"stop " + selfID, "rename " + selfID, "create nudge", "start new-1", "remove " + selfID},
		},
		{
			name: "new one exits", newState: container.State{Status: container.StateExited, ExitCode: 1}, fails: true,
			actions: []string{"stop " + selfID, "rename " + selfID, "create nudge", "start new-1", "remove new-1", "rename " + selfID, "start " + selfID},
		},
		{
			name:     "new one unhealthy",
			newState: container.State{Running: true, Status: container.StateRunning, Health: &container.Health{Status: container.Unhealthy}}, fails: true,
			actions: []string{"stop " + selfID, "rename " + selfID, "create nudge", "start new-1", "remove new-1", "rename " + selfID, "start " + selfID},
		},
		{
			name: "start fails", newState: running, failOn: "start new-1", fails: true,
			actions: []string{"stop " + selfID, "rename " + selfID, "create nudge", "remove new-1", "rename " + selfID, "start " + selfID},
		},
		{
			name: "create fails", newState: running, failOn: "create nudge", fails: true,
			actions: []string{"stop " + selfID, "rename " + selfID, "rename " + selfID, "start " + selfID},
		},
		{
			name: "leftover removed", newState: running, aside: &container.State{Status: container.StateExited},
			actions: []string{"remove aside", "stop " + selfID, "rename " + selfID, "create nudge", "start new-1", "remove " + selfID},
		},
		{
			name: "leftover running", newState: running, aside: &running, fails: true,
		},
		{
			name: "already current", newState: running, want: ErrCurrent,
			old: func(c *container.InspectResponse) { c.Image = "sha256:new" },
		},
		{
			name: "removed on stop", newState: running, want: ErrAutoRemove,
			old: func(c *container.InspectResponse) { c.HostConfig.AutoRemove = true },
		},
		{
			name: "pinned", newState: running, want: ErrPinned,
			old: func(c *container.InspectResponse) { c.Config.Image = "nudge@sha256:abc" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			old := self()
			if tt.old != nil {
				tt.old(&old)
			}
			f := newFake(old)
			f.ContainerDetails["new-1"] = container.InspectResponse{ID: "new-1", State: &tt.newState}
			if tt.aside != nil {
				f.ContainerDetails["nudge-nudge-old"] = container.InspectResponse{ID: "aside", State: tt.aside}
				f.ContainerDetails["aside"] = f.ContainerDetails["nudge-nudge-old"]
			}
			f.FailAction = func(a dockertest.Action) error {
				if a.Name+" "+a.ID == tt.failOn {
					return cerrdefs.ErrUnavailable
				}
				return nil
			}
			const limit = time.Hour
			err := Swap(t.Context(), f, selfID, SwapOptions{After: instant(limit), Limit: limit, Settle: 3 * time.Second, Poll: time.Second})
			if (tt.fails && err == nil) || (!tt.fails && !errors.Is(err, tt.want)) {
				t.Fatalf("Swap = %v, want %v (fails %t)", err, tt.want, tt.fails)
			}
			var got []string
			for _, a := range f.Actions() {
				got = append(got, a.Name+" "+a.ID)
			}
			if !slices.Equal(got, tt.actions) {
				t.Errorf("actions = %q, want %q", got, tt.actions)
			}
		})
	}
}

func TestSwapKeepsSettings(t *testing.T) {
	t.Parallel()
	old := self()
	old.Config.Env = []string{"PATH=/bin", "NUDGE_ADDR=:9000"}
	old.HostConfig.Binds = []string{"/run/docker.sock:/var/run/docker.sock"}
	f := newFake(old)
	f.ImageDetails["sha256:old"] = image.InspectResponse{ID: "sha256:old", Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Env: []string{"PATH=/bin"}}}}
	f.ContainerDetails["new-1"] = container.InspectResponse{ID: "new-1", State: &container.State{Running: true, Status: container.StateRunning}}
	const limit = time.Hour
	if err := Swap(t.Context(), f, selfID, SwapOptions{After: instant(limit), Limit: limit, Settle: time.Second, Poll: time.Second}); err != nil {
		t.Fatal(err)
	}
	var created client.ContainerCreateOptions
	for _, a := range f.Actions() {
		if a.Name == "create" {
			created = a.Options.(client.ContainerCreateOptions)
		}
	}
	if created.Name != "nudge" || created.Config.Image != ref || !slices.Equal(created.Config.Env, []string{"NUDGE_ADDR=:9000"}) ||
		!slices.Equal(created.HostConfig.Binds, old.HostConfig.Binds) || created.Config.Hostname != "" {
		t.Errorf("created = %+v / %+v", created.Config, created.HostConfig)
	}
}

func TestRemoteSelf(t *testing.T) {
	t.Parallel()
	notFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	tests := []struct {
		name    string
		handler http.Handler
		want    error
	}{
		{name: "new agent", handler: Control(stacks.Self{ID: selfID, Hostname: hostname}, "unix:///var/run/docker.sock", notFound)},
		{name: "old agent", handler: notFound, want: ErrOldAgent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)
			dial := func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
			}
			s, host, err := RemoteSelf(t.Context(), dial)
			if !errors.Is(err, tt.want) {
				t.Fatalf("RemoteSelf = %v, want %v", err, tt.want)
			}
			if tt.want == nil && (s.ID != selfID || s.Hostname != hostname || host != "unix:///var/run/docker.sock") {
				t.Errorf("RemoteSelf = %+v, %q", s, host)
			}
		})
	}
	// Other control requests still reach the stacks service.
	srv := httptest.NewServer(Control(stacks.Self{}, "", notFound))
	t.Cleanup(srv.Close)
	resp, err := srv.Client().Get(srv.URL + "/v1/stacks/web")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /v1/stacks/web = %d, want next's 404", resp.StatusCode)
	}
}

func TestSwapRetriesTheOldStart(t *testing.T) {
	t.Parallel()
	f := newFake(self())
	f.ContainerDetails["new-1"] = container.InspectResponse{ID: "new-1", State: &container.State{Status: container.StateExited}}
	starts := 0
	f.FailAction = func(a dockertest.Action) error {
		// The new container still holds the port for the first two tries.
		if a.Name == "start" && a.ID == selfID {
			if starts++; starts <= 2 {
				return cerrdefs.ErrConflict
			}
		}
		return nil
	}
	const limit = time.Hour
	if err := Swap(t.Context(), f, selfID, SwapOptions{After: instant(limit), Limit: limit, Settle: time.Second, Poll: time.Second}); err == nil {
		t.Fatal("Swap = nil, want the new container's failure")
	}
	if got := f.Actions(); got[len(got)-1].Name != "start" || got[len(got)-1].ID != selfID || starts != 3 {
		t.Errorf("old container started after %d tries, last action %+v", starts, got[len(got)-1])
	}
}

// Containers installed before the disk size get Docker's root dir mounted where they
// look for it; existing mounts and a turned-off disk size are left alone.
func TestSwapMountsDockerRoot(t *testing.T) {
	t.Parallel()
	base := []string{"/run/docker.sock:/var/run/docker.sock"}
	tests := []struct {
		name    string
		old     func(*container.InspectResponse)
		root    string
		infoErr error
		want    []string
	}{
		{"added", nil, "/var/lib/docker", nil, append(slices.Clone(base), "/var/lib/docker:/host/docker:ro")},
		{"other data-root", nil, "/srv/docker", nil, append(slices.Clone(base), "/srv/docker:/host/docker:ro")},
		{"env target", func(c *container.InspectResponse) { c.Config.Env = []string{"NUDGE_DOCKER_ROOT=/mnt/docker/"} },
			"/var/lib/docker", nil, append(slices.Clone(base), "/var/lib/docker:/mnt/docker:ro")},
		{"flag target", func(c *container.InspectResponse) {
			c.Config.Env = []string{"NUDGE_DOCKER_ROOT=/mnt/docker"}
			c.Config.Cmd = []string{"agent", "--docker-root", "/srv/root"}
		}, "/var/lib/docker", nil, append(slices.Clone(base), "/var/lib/docker:/srv/root:ro")},
		{"flag with =", func(c *container.InspectResponse) { c.Config.Cmd = []string{"-docker-root=/srv/root"} },
			"/var/lib/docker", nil, append(slices.Clone(base), "/var/lib/docker:/srv/root:ro")},
		{"last env wins", func(c *container.InspectResponse) {
			c.Config.Env = []string{"NUDGE_DOCKER_ROOT=/a", "NUDGE_DOCKER_ROOT=/b"}
		}, "/var/lib/docker", nil, append(slices.Clone(base), "/var/lib/docker:/b:ro")},
		{"entrypoint flag", func(c *container.InspectResponse) {
			c.Config.Entrypoint = []string{"nudge", "serve", "-docker-root=/srv/root"}
		}, "/var/lib/docker", nil, append(slices.Clone(base), "/var/lib/docker:/srv/root:ro")},
		{"turned off", func(c *container.InspectResponse) { c.Config.Env = []string{"NUDGE_DOCKER_ROOT="} }, "/var/lib/docker", nil, base},
		{"already mounted", func(c *container.InspectResponse) {
			c.Mounts = append(c.Mounts, container.MountPoint{Type: "bind", Source: "/data/docker", Destination: "/host/docker"})
		}, "/var/lib/docker", nil, base},
		{"parent mounted", func(c *container.InspectResponse) {
			c.Mounts = append(c.Mounts, container.MountPoint{Type: "bind", Source: "/", Destination: "/host"})
		}, "/var/lib/docker", nil, base},
		{"root unknown", nil, "", nil, base},
		{"info fails", nil, "", cerrdefs.ErrUnavailable, base},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			old := self()
			old.HostConfig.Binds = slices.Clone(base)
			if tt.old != nil {
				tt.old(&old)
			}
			f := newFake(old)
			f.InfoResult = client.SystemInfoResult{Info: system.Info{DockerRootDir: tt.root}}
			f.InfoErr = tt.infoErr
			f.ContainerDetails["new-1"] = container.InspectResponse{ID: "new-1", State: &container.State{Running: true, Status: container.StateRunning}}
			const limit = time.Hour
			if err := Swap(t.Context(), f, selfID, SwapOptions{After: instant(limit), Limit: limit, Settle: time.Second, Poll: time.Second}); err != nil {
				t.Fatal(err)
			}
			for _, a := range f.Actions() {
				if a.Name == "create" {
					if got := a.Options.(client.ContainerCreateOptions).HostConfig.Binds; !slices.Equal(got, tt.want) {
						t.Errorf("binds = %q, want %q", got, tt.want)
					}
				}
			}
		})
	}
}
