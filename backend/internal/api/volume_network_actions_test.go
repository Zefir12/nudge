package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestVolumeNetworkActions(t *testing.T) {
	t.Parallel()
	ipv6 := true
	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		fake     *dockertest.Fake
		wantCode int
		wantErr  string
		wantBody string
		want     []dockertest.Action
	}{
		{name: "create volume", method: "POST", path: "/volumes", wantCode: 201,
			body:     `{"name":"media","driver":"local","options":{"type":"cifs","o":"password=secret-pw"},"labels":{"app":"web"}}`,
			wantBody: `{"name":"media","driver":"local","mountpoint":"","scope":"local","created_at":null,"labels":{"app":"web"},"size":null,"size_measured_at":null}`,
			want: []dockertest.Action{{Name: "volume-create", ID: "media", Options: client.VolumeCreateOptions{
				Name: "media", Driver: "local", DriverOpts: map[string]string{"type": "cifs", "o": "password=secret-pw"}, Labels: map[string]string{"app": "web"},
			}}}},
		{name: "create anonymous volume", method: "POST", path: "/volumes", body: `{}`, wantCode: 201,
			wantBody: `{"name":"generated","driver":"local","mountpoint":"","scope":"local","created_at":null,"labels":{},"size":null,"size_measured_at":null}`,
			want:     []dockertest.Action{{Name: "volume-create", Options: client.VolumeCreateOptions{}}}},
		{name: "create volume invalid name", method: "POST", path: "/volumes", body: `{"name":"../x"}`, wantCode: 400,
			wantErr: "name may only contain letters, digits, '_', '.' and '-' and must start with a letter or digit"},
		{name: "create volume invalid driver", method: "POST", path: "/volumes", body: `{"driver":"a b"}`, wantCode: 400, wantErr: "invalid driver name"},
		{name: "create volume empty option", method: "POST", path: "/volumes", body: `{"options":{"":"x"}}`, wantCode: 400, wantErr: "option names must not be empty"},
		{name: "create volume empty label", method: "POST", path: "/volumes", body: `{"labels":{"":"x"}}`, wantCode: 400, wantErr: "label names must not be empty"},
		{name: "create volume malformed", method: "POST", path: "/volumes", body: `{`, wantCode: 400, wantErr: "invalid JSON body: malformed"},
		{name: "create volume unknown driver", method: "POST", path: "/volumes", body: `{"driver":"nope"}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: fmt.Errorf("secret-pw: %w", cerrdefs.ErrNotFound)}},
			wantCode: 400, wantErr: "the Docker host has no such volume driver"},
		{name: "create volume refused", method: "POST", path: "/volumes", body: `{"options":{"o":"x"}}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: fmt.Errorf("secret-pw: %w", cerrdefs.ErrInvalidArgument)}},
			wantCode: 400, wantErr: "the Docker host refused the volume settings"},
		{name: "create volume other driver", method: "POST", path: "/volumes", body: `{"name":"data"}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: cerrdefs.ErrConflict}},
			wantCode: 409, wantErr: "a volume with this name already exists with another driver"},
		{name: "create volume offline", method: "POST", path: "/volumes", body: `{}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: &net.OpError{Op: "dial", Err: errors.New("refused")}}},
			wantCode: 503, wantErr: "the environment is offline"},

		{name: "remove volume", method: "DELETE", path: "/volumes/data", wantCode: 204,
			want: []dockertest.Action{{Name: "volume-remove", ID: "data", Options: client.VolumeRemoveOptions{}}}},
		{name: "remove volume force", method: "DELETE", path: "/volumes/data?force=true", wantCode: 204,
			want: []dockertest.Action{{Name: "volume-remove", ID: "data", Options: client.VolumeRemoveOptions{Force: true}}}},
		{name: "remove volume unknown", method: "DELETE", path: "/volumes/nope", wantCode: 404, wantErr: "volume not found"},
		{name: "remove volume invalid id", method: "DELETE", path: "/volumes/..%2F..", wantCode: 404, wantErr: "volume not found"},
		{name: "remove volume invalid force", method: "DELETE", path: "/volumes/data?force=x", wantCode: 400, wantErr: "force must be true or false"},
		{name: "remove volume in use", method: "DELETE", path: "/volumes/data",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: fmt.Errorf("secret-detail: %w", cerrdefs.ErrConflict)}},
			wantCode: 409, wantErr: "the volume is in use by a container"},

		{name: "prune anonymous volumes", method: "POST", path: "/volumes/prune",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{VolumePruneReport: volume.PruneReport{VolumesDeleted: []string{"a1"}, SpaceReclaimed: 512}}},
			wantCode: 200, wantBody: `{"deleted":["a1"],"space_reclaimed":512}`,
			want: []dockertest.Action{{Name: "volume-prune", Options: client.VolumePruneOptions{}}}},
		{name: "prune all volumes", method: "POST", path: "/volumes/prune?all=true", wantCode: 200,
			wantBody: `{"deleted":[],"space_reclaimed":0}`,
			want:     []dockertest.Action{{Name: "volume-prune", Options: client.VolumePruneOptions{All: true}}}},
		{name: "prune volumes invalid all", method: "POST", path: "/volumes/prune?all=1x", wantCode: 400, wantErr: "all must be true or false"},
		{name: "prune volumes already running", method: "POST", path: "/volumes/prune",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: cerrdefs.ErrConflict}},
			wantCode: 409, wantErr: "a volume prune is already running"},

		{name: "create network", method: "POST", path: "/networks", wantCode: 201,
			body:     `{"name":"apps","driver":"bridge","internal":true,"attachable":true,"ipv6":true,"subnet":"172.30.0.0/16","gateway":"172.30.0.1","ip_range":"172.30.5.0/24","options":{"com.docker.network.bridge.name":"br-apps"},"labels":{"app":"web"}}`,
			wantBody: `{"id":"id-apps","warnings":[]}`,
			want: []dockertest.Action{{Name: "network-create", ID: "apps", Options: client.NetworkCreateOptions{
				Driver: "bridge", Internal: true, Attachable: true, EnableIPv6: &ipv6,
				IPAM: &network.IPAM{Config: []network.IPAMConfig{{
					Subnet: netip.MustParsePrefix("172.30.0.0/16"), Gateway: netip.MustParseAddr("172.30.0.1"), IPRange: netip.MustParsePrefix("172.30.5.0/24"),
				}}},
				Options: map[string]string{"com.docker.network.bridge.name": "br-apps"}, Labels: map[string]string{"app": "web"},
			}}}},
		{name: "create network defaults", method: "POST", path: "/networks", body: `{"name":"apps"}`, wantCode: 201,
			wantBody: `{"id":"id-apps","warnings":[]}`,
			want:     []dockertest.Action{{Name: "network-create", ID: "apps", Options: client.NetworkCreateOptions{}}}},
		{name: "create network without name", method: "POST", path: "/networks", body: `{}`, wantCode: 400, wantErr: "name is required"},
		{name: "create network invalid name", method: "POST", path: "/networks", body: `{"name":"a/b"}`, wantCode: 400,
			wantErr: "name may only contain letters, digits, '_', '.' and '-' and must start with a letter or digit"},
		{name: "create network invalid subnet", method: "POST", path: "/networks", body: `{"name":"a","subnet":"172.30.0.1/16"}`, wantCode: 400,
			wantErr: "subnet must be a network in CIDR notation, such as 172.30.0.0/16"},
		{name: "create network gateway outside", method: "POST", path: "/networks", body: `{"name":"a","subnet":"172.30.0.0/16","gateway":"10.0.0.1"}`, wantCode: 400,
			wantErr: "gateway must be an address in the subnet"},
		{name: "create network range outside", method: "POST", path: "/networks", body: `{"name":"a","subnet":"172.30.0.0/16","ip_range":"172.0.0.0/8"}`, wantCode: 400,
			wantErr: "ip_range must be a network in CIDR notation inside the subnet"},
		{name: "create network gateway without subnet", method: "POST", path: "/networks", body: `{"name":"a","gateway":"10.0.0.1"}`, wantCode: 400,
			wantErr: "gateway and ip_range need a subnet"},
		{name: "create network exists", method: "POST", path: "/networks", body: `{"name":"front"}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: cerrdefs.ErrConflict}},
			wantCode: 409, wantErr: "a network with this name already exists"},
		{name: "create network overlap", method: "POST", path: "/networks", body: `{"name":"a","subnet":"172.17.0.0/16"}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: fmt.Errorf("secret-detail: %w", cerrdefs.ErrPermissionDenied)}},
			wantCode: 400, wantErr: "the Docker host refused the network settings"},

		{name: "remove network", method: "DELETE", path: "/networks/front", wantCode: 204,
			want: []dockertest.Action{{Name: "network-remove", ID: "front", Options: client.NetworkRemoveOptions{}}}},
		{name: "remove network unknown", method: "DELETE", path: "/networks/nope", wantCode: 404, wantErr: "network not found"},
		{name: "remove network in use", method: "DELETE", path: "/networks/front",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: fmt.Errorf("secret-detail: %w", cerrdefs.ErrPermissionDenied)}},
			wantCode: 409, wantErr: "the network has containers attached or is one of Docker's predefined networks"},
		{name: "remove network daemon error", method: "DELETE", path: "/networks/front",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: errors.New("secret-detail")}},
			wantCode: 502},

		{name: "prune networks", method: "POST", path: "/networks/prune",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{NetworkPruneReport: network.PruneReport{NetworksDeleted: []string{"old"}}}},
			wantCode: 200, wantBody: `{"deleted":["old"]}`,
			want: []dockertest.Action{{Name: "network-prune", Options: client.NetworkPruneOptions{}}}},
		{name: "prune networks none", method: "POST", path: "/networks/prune", wantCode: 200, wantBody: `{"deleted":[]}`,
			want: []dockertest.Action{{Name: "network-prune", Options: client.NetworkPruneOptions{}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := tt.fake
			if fake == nil {
				fake = &dockertest.Fake{}
			}
			fake.Volumes = []volume.Volume{{Name: "data"}}
			fake.NetworkDetails = map[string]network.Inspect{"front": {}}
			et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
			path := fmt.Sprintf("/api/v1/environments/%d%s", et.ids["local"], tt.path)
			rec := do(t, et.h, request{method: tt.method, path: path, body: tt.body, cookie: et.cookie})
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			switch {
			case tt.wantCode == http.StatusNoContent:
				if rec.Body.Len() != 0 {
					t.Errorf("body = %q, want none", rec.Body)
				}
			case tt.wantBody != "":
				if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
					t.Errorf("body = %s, want %s", got, tt.wantBody)
				}
			default:
				var body struct{ Error string }
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
					t.Fatalf("body = %s, want a JSON error", rec.Body)
				}
				if tt.wantErr != "" && body.Error != tt.wantErr {
					t.Errorf("error = %q, want %q", body.Error, tt.wantErr)
				}
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("response leaks secrets: %s", rec.Body)
			}
			if got := fake.Actions(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("actions = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestVolumeNetworkActionsNeedSession(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		Volumes:        []volume.Volume{{Name: "data"}},
		NetworkDetails: map[string]network.Inspect{"front": {}},
	}}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
	base := fmt.Sprintf("/api/v1/environments/%d", et.ids["local"])
	for _, req := range []request{
		{method: "POST", path: base + "/volumes", body: `{}`},
		{method: "POST", path: base + "/volumes/prune"},
		{method: "DELETE", path: base + "/volumes/data"},
		{method: "POST", path: base + "/networks", body: `{"name":"a"}`},
		{method: "POST", path: base + "/networks/prune"},
		{method: "DELETE", path: base + "/networks/front"},
	} {
		if rec := do(t, et.h, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session: code = %d, want 401", req.method, req.path, rec.Code)
		}
	}
	if got := fake.Actions(); len(got) != 0 {
		t.Errorf("actions = %+v, want none", got)
	}
}

func TestCreateErrorHidesDaemonMessage(t *testing.T) {
	t.Parallel()
	timeout := fmt.Errorf("secret-pw: %w", context.DeadlineExceeded)
	offline := &net.OpError{Op: "dial", Err: errors.New("refused")}
	for _, err := range []error{
		errors.New("Error response from daemon: mount o=password=secret-pw failed"),
		fmt.Errorf("secret-pw: %w", cerrdefs.ErrNotFound),
		fmt.Errorf("secret-pw: %w", cerrdefs.ErrInvalidArgument),
	} {
		if got := createError(err, "volume"); strings.Contains(got.Error(), "secret") {
			t.Errorf("createError(%q) = %q, leaks the daemon message", err, got)
		}
	}
	// Errors runDocker answers by kind keep it.
	for _, err := range []error{timeout, offline, cerrdefs.ErrConflict} {
		if got := createError(err, "volume"); !errors.Is(got, err) {
			t.Errorf("createError(%v) = %v, want it unchanged", err, got)
		}
	}
}
