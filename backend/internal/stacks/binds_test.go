package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
			"web":{"volumes":[{"type":"bind","source":"/s/web/data","target":"/data"},{"type":"bind","source":"/s/web","target":"/app"}]},
			"db":{"volumes":[{"type":"bind","source":"/s/web/data/","target":"/x"}]}}}`,
			[]string{"/s/web", "/s/web/data"}},
		{"volumes and outside binds left out", `{"services":{"web":{"volumes":[
			{"type":"volume","source":"data","target":"/data"},
			{"type":"bind","source":"/var/run/docker.sock","target":"/var/run/docker.sock"},
			{"type":"bind","source":"/s/webapp","target":"/a"},
			{"type":"bind","source":"/s/other/x","target":"/b"},
			{"type":"tmpfs","target":"/tmp"}]}}}`, []string{}},
		{"secrets and configs", `{"services":{},"secrets":{"pw":{"file":"/s/web/pw.txt"},"ext":{"external":true}},
			"configs":{"conf":{"file":"/s/web/nginx.conf"},"env":{"environment":"X"}}}`,
			[]string{"/s/web/nginx.conf", "/s/web/pw.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := bindSources([]byte(tt.config), "/s/web")
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bindSources = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := bindSources([]byte("not json"), "/s/web"); err == nil {
		t.Error("bindSources of invalid JSON: no error")
	}
}

// configExec answers `compose config` with config and exit code configCode, and records
// the subcommand of every run.
type configExec struct {
	config     string
	configCode int
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
		return e.configCode, nil
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
			_, err := NewLocal(opts).Up(context.Background(), "web", false, func(Line) {})
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

func TestLocalBindCheckErrors(t *testing.T) {
	t.Parallel()
	d := NewDir(t.TempDir())
	if err := d.Create("web", Files{Compose: "services: {}\n"}); err != nil {
		t.Fatal(err)
	}
	inspectErr := errors.New("daemon gone")
	tests := []struct {
		name   string
		config string
		mounts MountsFunc
		want   error
	}{
		{"inspect fails", `{"services":{"web":{"volumes":[{"type":"bind","source":"` + d.Path("web") + `/x"}]}}}`,
			func(context.Context) ([]Mount, bool, error) { return nil, false, inspectErr }, inspectErr},
		{"invalid config output", `{`, func(context.Context) ([]Mount, bool, error) { return nil, true, nil }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ex := &configExec{config: tt.config}
			_, err := NewLocal(LocalOptions{Dir: d, Exec: ex.exec, Mounts: tt.mounts}).Up(context.Background(), "web", false, func(Line) {})
			var bind *BindError
			if err == nil || errors.As(err, &bind) || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Errorf("Up = %v, want a plain error (%v)", err, tt.want)
			}
			if slices.Contains(ex.calls, "up") {
				t.Error("up ran after the check failed")
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
		want  string
	}{
		{"docker", map[string]string{"/proc/self/mountinfo": container, "/.dockerenv": ""}, id},
		{"podman", map[string]string{"/proc/self/mountinfo": podman}, id},
		{"host with containers", map[string]string{"/proc/self/mountinfo": host}, ""},
		{"no mountinfo", map[string]string{}, ""},
		{"hostname fallback", map[string]string{"/proc/self/mountinfo": host, "/.dockerenv": ""}, "nudge-1"},
		{"podman marker", map[string]string{"/run/.containerenv": ""}, "nudge-1"},
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
			if got := SelfID(read, func() (string, error) { return "nudge-1\n", nil }); got != tt.want {
				t.Errorf("SelfID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainerMounts(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{}
	fake.ContainerDetails = map[string]container.InspectResponse{"self": {Mounts: []container.MountPoint{
		{Type: "volume", Name: "nudge-data", Source: "/var/lib/docker/volumes/nudge-data/_data", Destination: "/data"},
		{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
	}}}
	connectErr := errors.New("no client")
	tests := []struct {
		name    string
		id      string
		connect error
		want    []Mount
		in      bool
		err     error
	}{
		{name: "mounts", id: "self", in: true, want: []Mount{
			{Type: "volume", Name: "nudge-data", Source: "/var/lib/docker/volumes/nudge-data/_data", Destination: "/data"},
			{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
		}},
		{name: "not in a container", id: ""},
		{name: "unknown to the daemon", id: "other"},
		{name: "no client", id: "self", connect: connectErr, err: connectErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mounts := ContainerMounts(tt.id, func() (Inspector, error) {
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
