package stacks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestBindSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config string
		want   []string
	}{
		{"none", `{"services":{"web":{"image":"nginx"}}}`, []string{}},
		{"binds under the directory", `{"services":{
			"web":{"volumes":[{"type":"bind","source":"/s/stacks/web/data","target":"/data"},{"type":"bind","source":"/s/stacks/web","target":"/app"}]},
			"db":{"volumes":[{"type":"bind","source":"/s/stacks/web/data/","target":"/x"}]}}}`,
			[]string{"/s/stacks/web", "/s/stacks/web/data"}},
		{"volumes and outside binds left out", `{"services":{"web":{"volumes":[
			{"type":"volume","source":"data","target":"/data"},
			{"type":"bind","source":"/var/run/docker.sock","target":"/var/run/docker.sock"},
			{"type":"bind","source":"/stacks","target":"/a"},
			{"type":"bind","source":"/s/stacksx/web","target":"/b"},
			{"type":"tmpfs","target":"/tmp"}]}}}`, []string{}},
		{"other stacks under the root", `{"services":{"web":{"volumes":[
			{"type":"bind","source":"/s/stacks/shared/data","target":"/data"}]}}}`, []string{"/s/stacks/shared/data"}},
		{"used secrets and configs", `{"services":{"web":{"secrets":[{"source":"pw","target":"/run/secrets/pw"},{"source":"ext"}],
			"configs":[{"source":"conf","target":"/etc/nginx.conf"}]}},
			"secrets":{"pw":{"file":"/s/stacks/web/pw.txt"},"ext":{"external":true}},
			"configs":{"conf":{"file":"/s/stacks/web/nginx.conf"},"env":{"environment":"X"}}}`,
			[]string{"/s/stacks/web/nginx.conf", "/s/stacks/web/pw.txt"}},
		{"unused secrets and configs", `{"services":{"web":{"image":"nginx"}},
			"secrets":{"pw":{"file":"/s/stacks/web/pw.txt"}},"configs":{"conf":{"file":"/s/stacks/web/nginx.conf"}}}`,
			[]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := bindSources([]byte(tt.config), "/s/stacks")
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bindSources = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := bindSources([]byte("not json"), "/s/stacks"); err == nil {
		t.Error("bindSources of invalid JSON: no error")
	}
}

// configExec answers `compose config` with config and exit code configCode, and records
// the subcommand of every run.
type configExec struct {
	config     string
	configCode int
	configErr  error
	calls      []string
}

func (e *configExec) exec(_ context.Context, cmd Command) (int, error) {
	i := slices.Index(cmd.Args, "plain")
	sub := cmd.Args[i+1]
	e.calls = append(e.calls, sub)
	if sub == "config" {
		if !slices.Equal(cmd.Args[i+1:], []string{"config", "--format", "json"}) {
			return 0, fmt.Errorf("config args %q", cmd.Args[i+1:])
		}
		_, _ = cmd.Stdout.Write([]byte(e.config))
		return e.configCode, e.configErr
	}
	return 0, nil
}

func TestLocalChecksBinds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// binds are the bind sources compose reports, relative to the project directory
		// ("" is the directory itself) or absolute.
		binds      []string
		configCode int
		// mounts are nudge's mounts; %[1]s is the stacks directory, %[2]s its parent.
		mounts    []Mount
		noMounts  bool // no Mounts option
		outside   bool // not in a container
		wantCalls []string
		// wantErr is part of the BindError's message, "" for none.
		wantErr string
	}{
		{name: "no check without Mounts", binds: []string{"data"}, noMounts: true, wantCalls: []string{"up"}},
		{name: "no binds", mounts: []Mount{{Type: "volume", Name: "v", Destination: "%[2]s"}},
			wantCalls: []string{"config", "up"}},
		{name: "outside a container", binds: []string{"data"}, outside: true, wantCalls: []string{"config", "up"}},
		{name: "stacks in a volume", binds: []string{"data"},
			mounts:    []Mount{{Type: "volume", Name: "nudge-data", Source: "/var/lib/docker/volumes/nudge-data/_data", Destination: "%[2]s"}},
			wantCalls: []string{"config"}, wantErr: "the bind mount ./data can't be deployed: the daemon takes %[1]s/web/data from the host, " +
				"but nudge has it from the volume nudge-data mounted at %[2]s; mount the stacks directory at the same path on the host and set NUDGE_STACKS_DIR to it"},
		{name: "project directory bound", binds: []string{""},
			mounts:    []Mount{{Type: "volume", Name: "nudge-data", Destination: "%[2]s"}},
			wantCalls: []string{"config"}, wantErr: "the bind mount . can't"},
		{name: "another stack's directory", binds: []string{"../shared/data"},
			mounts:    []Mount{{Type: "volume", Name: "nudge-data", Destination: "%[2]s"}},
			wantCalls: []string{"config"}, wantErr: "the bind mount ../shared/data can't be deployed: the daemon takes %[1]s/shared/data from"},
		{name: "same path on the host", binds: []string{"data", "conf/nginx.conf"},
			mounts:    []Mount{{Type: "volume", Name: "nudge-data", Destination: "%[2]s"}, {Type: "bind", Source: "%[1]s", Destination: "%[1]s/"}},
			wantCalls: []string{"config", "up"}},
		{name: "parent bound at the same path", binds: []string{"data"},
			mounts:    []Mount{{Type: "bind", Source: "%[2]s", Destination: "%[2]s"}},
			wantCalls: []string{"config", "up"}},
		{name: "another host path", binds: []string{"data"},
			mounts:    []Mount{{Type: "bind", Source: "/srv/nudge", Destination: "%[2]s"}},
			wantCalls: []string{"config"}, wantErr: "but nudge has it from the host's /srv/nudge mounted at %[2]s"},
		{name: "no mount", binds: []string{"data"}, mounts: []Mount{{Type: "bind", Source: "/x", Destination: "/x"}},
			wantCalls: []string{"config"}, wantErr: "nudge has it from its own filesystem, not from a mount"},
		{name: "tmpfs", binds: []string{"data"}, mounts: []Mount{{Type: "tmpfs", Destination: "%[2]s"}},
			wantCalls: []string{"config"}, wantErr: "from a tmpfs mount at %[2]s"},
		{name: "outside binds pass", binds: []string{"/var/run/docker.sock", "/srv/web"},
			mounts:    []Mount{{Type: "volume", Name: "nudge-data", Destination: "%[2]s"}},
			wantCalls: []string{"config", "up"}},
		{name: "compose rejects the project", binds: []string{"data"}, configCode: 15,
			mounts: []Mount{{Type: "volume", Name: "nudge-data", Destination: "%[2]s"}}, wantCalls: []string{"config", "up"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parent := t.TempDir()
			root := parent + "/stacks"
			d := NewDir(root)
			if err := d.Create("web", Files{Compose: "services: {}\n"}); err != nil {
				t.Fatal(err)
			}
			dir := d.Path("web")
			sub := strings.NewReplacer("%[1]s", root, "%[2]s", parent).Replace
			var volumes []string
			for _, b := range tt.binds {
				src := b
				if !strings.HasPrefix(b, "/") {
					src = strings.TrimSuffix(dir+"/"+b, "/")
				}
				volumes = append(volumes, fmt.Sprintf(`{"type":"bind","source":%q,"target":"/t"}`, src))
			}
			ex := &configExec{
				config:     `{"services":{"web":{"volumes":[` + strings.Join(volumes, ",") + `]}}}`,
				configCode: tt.configCode,
			}
			opts := LocalOptions{Dir: d, Exec: ex.exec}
			if !tt.noMounts {
				opts.Mounts = func(context.Context) ([]Mount, bool, error) {
					var ms []Mount
					for _, m := range tt.mounts {
						m.Source = sub(m.Source)
						m.Destination = sub(m.Destination)
						ms = append(ms, m)
					}
					return ms, !tt.outside, nil
				}
			}
			_, err := NewLocal(opts).Up(context.Background(), "web", UpOptions{}, func(Line) {})
			if !slices.Equal(ex.calls, tt.wantCalls) {
				t.Errorf("compose runs = %q, want %q", ex.calls, tt.wantCalls)
			}
			var bind *BindError
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Up = %v, want no error", err)
			case tt.wantErr != "" && !errors.As(err, &bind):
				t.Errorf("Up = %v, want a BindError", err)
			case tt.wantErr != "" && !strings.Contains(err.Error(), sub(tt.wantErr)):
				t.Errorf("Up = %q, want it to contain %q", err, sub(tt.wantErr))
			}
		})
	}
}

func TestLocalBindCheckFailures(t *testing.T) {
	t.Parallel()
	d := NewDir(t.TempDir())
	if err := d.Create("web", Files{Compose: "services: {}\n"}); err != nil {
		t.Fatal(err)
	}
	bind := `{"services":{"web":{"volumes":[{"type":"bind","source":"` + d.Path("web") + `/x"}]}}}`
	volume := func(context.Context) ([]Mount, bool, error) {
		return []Mount{{Type: "volume", Name: "v", Destination: "/"}}, true, nil
	}
	tests := []struct {
		name      string
		config    string
		configErr error
		mounts    MountsFunc
	}{
		{"inspect fails", bind, nil, func(context.Context) ([]Mount, bool, error) { return nil, false, errors.New("daemon gone") }},
		{"invalid config output", `{`, nil, volume},
		{"oversized config output", bind + strings.Repeat(" ", maxConfigOutput), nil, volume},
		{"compose config fails to run", bind, errors.New("killed"), volume},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			ex := &configExec{config: tt.config, configErr: tt.configErr}
			l := NewLocal(LocalOptions{Dir: d, Exec: ex.exec, Mounts: tt.mounts, Log: slog.New(slog.NewTextHandler(&logs, nil))})
			if _, err := l.Up(context.Background(), "web", UpOptions{}, func(Line) {}); err != nil {
				t.Errorf("Up = %v, want the deploy to go on", err)
			}
			if !slices.Equal(ex.calls, []string{"config", "up"}) {
				t.Errorf("compose runs = %q, want config, up", ex.calls)
			}
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "stack=web") {
				t.Errorf("logs = %q, want a warning", logs.String())
			}
		})
	}
}

func TestSelfID(t *testing.T) {
	t.Parallel()
	const id = "3f4e0b8c0d1e2f3a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f901234"
	const other = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	container := "1 0 0:1 / / rw - overlay overlay rw\n" +
		"612 600 253:1 /var/lib/docker/containers/" + id + "/resolv.conf /etc/resolv.conf rw - ext4 /dev/vda1 rw\n" +
		"613 600 253:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw - ext4 /dev/vda1 rw\n"
	host := "1 0 0:1 / / rw - ext4 /dev/vda1 rw\n" +
		"80 1 0:50 / /var/lib/docker/containers/" + other + "/mounts/shm rw - tmpfs shm rw\n"
	podman := "700 600 0:40 /containers/storage/overlay-containers/" + id + "/userdata/hostname /etc/hostname rw - tmpfs tmpfs rw\n"
	tests := []struct {
		name  string
		files map[string]string
		host  string
		want  Self
	}{
		{"docker", map[string]string{"/proc/self/mountinfo": container, "/.dockerenv": ""}, "nudge-1", Self{ID: id, Hostname: "nudge-1"}},
		{"podman", map[string]string{"/proc/self/mountinfo": podman}, "3f4e0b8c0d1e", Self{ID: id, Hostname: "3f4e0b8c0d1e"}},
		{"host with containers", map[string]string{"/proc/self/mountinfo": host}, "3f4e0b8c0d1e", Self{}},
		{"no mountinfo", map[string]string{}, "3f4e0b8c0d1e", Self{}},
		{"short ID hostname", map[string]string{"/proc/self/mountinfo": host, "/.dockerenv": ""}, "3f4e0b8c0d1e\n",
			Self{ID: "3f4e0b8c0d1e", Hostname: "3f4e0b8c0d1e"}},
		{"podman marker", map[string]string{"/run/.containerenv": ""}, "3f4e0b8c0d1e", Self{ID: "3f4e0b8c0d1e", Hostname: "3f4e0b8c0d1e"}},
		{"hostname that is no ID", map[string]string{"/.dockerenv": ""}, "nudge-1", Self{}},
		{"hostname that is a name", map[string]string{"/.dockerenv": ""}, "web", Self{}},
		{"hostname with upper case", map[string]string{"/.dockerenv": ""}, "3F4E0B8C0D1E", Self{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			read := func(name string) ([]byte, error) {
				if s, ok := tt.files[name]; ok {
					return []byte(s), nil
				}
				return nil, fs.ErrNotExist
			}
			if got := SelfID(read, func() (string, error) { return tt.host, nil }); got != tt.want {
				t.Errorf("SelfID = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestContainerMounts(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{}
	fake.ContainerDetails = map[string]container.InspectResponse{"self": {
		Config: &container.Config{Hostname: "nudge-1"},
		Mounts: []container.MountPoint{
			{Type: "volume", Name: "nudge-data", Source: "/var/lib/docker/volumes/nudge-data/_data", Destination: "/data"},
			{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
		},
	}, "noconfig": {}}
	connectErr := errors.New("no client")
	tests := []struct {
		name    string
		self    Self
		connect error
		want    []Mount
		in      bool
		err     error
	}{
		{name: "mounts", self: Self{ID: "self", Hostname: "nudge-1"}, in: true, want: []Mount{
			{Type: "volume", Name: "nudge-data", Source: "/var/lib/docker/volumes/nudge-data/_data", Destination: "/data"},
			{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
		}},
		{name: "not in a container"},
		{name: "unknown to the daemon", self: Self{ID: "other", Hostname: "other"}},
		{name: "another container's hostname", self: Self{ID: "self", Hostname: "3f4e0b8c0d1e"}},
		{name: "no config", self: Self{ID: "noconfig", Hostname: "noconfig"}},
		{name: "no client", self: Self{ID: "self", Hostname: "nudge-1"}, connect: connectErr, err: connectErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mounts := ContainerMounts(tt.self, func() (Inspector, error) {
				if tt.connect != nil {
					return nil, tt.connect
				}
				return fake, nil
			})
			got, in, err := mounts(context.Background())
			if !reflect.DeepEqual(got, tt.want) || in != tt.in || !errors.Is(err, tt.err) {
				t.Errorf("mounts = %+v, %v, %v; want %+v, %v, %v", got, in, err, tt.want, tt.in, tt.err)
			}
		})
	}
}
