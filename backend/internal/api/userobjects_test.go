package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

// A user creates bridge networks and local volumes without options, named with their
// prefix and labelled as theirs; anything else is refused before Docker is asked.
func TestUserCreates(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	prefix := userPrefix(ot.bobID)
	// A deleted user's leftover with bob's prefix, and a volume bob already has.
	ot.fake.Volumes = append(ot.fake.Volumes,
		volume.Volume{Name: prefix + "old", Driver: "local", Labels: map[string]string{ownerLabel: "deleted-user"}},
		volume.Volume{Name: prefix + "data", Driver: "local", Labels: map[string]string{ownerLabel: ot.bobKey}},
	)
	bobs := map[string]string{ownerLabel: ot.bobKey}
	for _, tt := range []struct {
		name, path, body string
		cookie           *http.Cookie
		code             int
		want             string // the error, or the created object's name
		labels           map[string]string
	}{
		{"network", "/networks", `{"name":"web","driver":"bridge","internal":true}`, ot.bob, http.StatusCreated, prefix + "web", bobs},
		{"network default driver", "/networks", `{"name":"api","labels":{"app":"x"}}`, ot.bob, http.StatusCreated, prefix + "api",
			map[string]string{"app": "x", ownerLabel: ot.bobKey}},
		{"network admin", "/networks", `{"name":"web","driver":"macvlan"}`, ot.admin, http.StatusCreated, "web", nil},
		{"network no name", "/networks", `{"driver":"bridge"}`, ot.bob, http.StatusBadRequest, "name is required", nil},
		{"network macvlan", "/networks", `{"name":"n","driver":"macvlan"}`, ot.bob, http.StatusBadRequest, "users may only create bridge networks", nil},
		{"network host", "/networks", `{"name":"n","driver":"host"}`, ot.bob, http.StatusBadRequest, "users may only create bridge networks", nil},
		{"network plugin", "/networks", `{"name":"n","driver":"weaveworks/net-plugin:latest"}`, ot.bob, http.StatusBadRequest, "users may only create bridge networks", nil},
		{"network driver options", "/networks", `{"name":"n","options":{"com.docker.network.bridge.name":"eth0"}}`, ot.bob, http.StatusBadRequest, "users may not set network driver options", nil},
		{"network subnet", "/networks", `{"name":"n","subnet":"192.168.1.0/24"}`, ot.bob, http.StatusBadRequest, "users may not choose network addresses", nil},
		{"network ipv6", "/networks", `{"name":"n","ipv6":true}`, ot.bob, http.StatusBadRequest, "users may not enable IPv6", nil},
		{"network attachable", "/networks", `{"name":"n","attachable":true}`, ot.bob, http.StatusBadRequest, "users may not create attachable networks", nil},
		{"network name too long", "/networks", `{"name":"` + strings.Repeat("a", 255) + `"}`, ot.bob, http.StatusBadRequest, "name is too long", nil},
		{"volume", "/volumes", `{"name":"cache","driver":"local"}`, ot.bob, http.StatusCreated, prefix + "cache", bobs},
		{"volume own existing", "/volumes", `{"name":"data"}`, ot.bob, http.StatusCreated, prefix + "data", bobs},
		{"volume admin", "/volumes", `{"name":"cache","options":{"type":"tmpfs","device":"tmpfs"}}`, ot.admin, http.StatusCreated, "cache", nil},
		{"volume no name", "/volumes", `{}`, ot.bob, http.StatusBadRequest, "name is required", nil},
		{"volume driver", "/volumes", `{"name":"v","driver":"rexray/ebs"}`, ot.bob, http.StatusBadRequest, "users may only create local volumes", nil},
		{"volume bind", "/volumes", `{"name":"v","options":{"type":"none","o":"bind","device":"/etc"}}`, ot.bob, http.StatusBadRequest, "users may not set volume driver options", nil},
		{"volume device", "/volumes", `{"name":"v","options":{"device":"/dev/sda1"}}`, ot.bob, http.StatusBadRequest, "users may not set volume driver options", nil},
		{"volume nfs", "/volumes", `{"name":"v","options":{"type":"nfs","o":"addr=10.0.0.1"}}`, ot.bob, http.StatusBadRequest, "users may not set volume driver options", nil},
		{"volume another owner's", "/volumes", `{"name":"old"}`, ot.bob, http.StatusConflict, "a volume with this name already exists", nil},
	} {
		before := len(ot.fake.Actions())
		rec := do(t, ot.h, request{method: "POST", path: ot.path(ot.local, tt.path), cookie: tt.cookie, body: tt.body})
		if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s = %d %s, want %d %s", tt.name, rec.Code, rec.Body, tt.code, tt.want)
			continue
		}
		actions := ot.fake.Actions()[before:]
		if tt.code == http.StatusBadRequest {
			if len(actions) != 0 {
				t.Errorf("%s: actions %+v, want none", tt.name, actions)
			}
			continue
		}
		if tt.code != http.StatusCreated {
			continue
		}
		if len(actions) != 1 || actions[0].ID != tt.want {
			t.Errorf("%s: actions %+v, want one create of %s", tt.name, actions, tt.want)
			continue
		}
		var labels map[string]string
		switch o := actions[0].Options.(type) {
		case client.NetworkCreateOptions:
			labels = o.Labels
		case client.VolumeCreateOptions:
			labels = o.Labels
		}
		if len(labels) != 0 || len(tt.labels) != 0 {
			if !reflect.DeepEqual(labels, tt.labels) {
				t.Errorf("%s: labels %v, want %v", tt.name, labels, tt.labels)
			}
		}
	}
}

// A user has at most maxUserNetworks networks in an environment.
func TestUserNetworkLimit(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	// Bob owns "mine"; fill up to one below the limit.
	for i := range maxUserNetworks - 2 {
		n := network.Network{ID: fmt.Sprint("n", i), Name: fmt.Sprint("n", i), Labels: map[string]string{ownerLabel: ot.bobKey}}
		ot.fake.Networks = append(ot.fake.Networks, network.Summary{Network: n})
	}
	create := func() *httptest.ResponseRecorder {
		return do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/networks"), cookie: ot.bob, body: `{"name":"web"}`})
	}
	if rec := create(); rec.Code != http.StatusCreated {
		t.Fatalf("create below the limit = %d %s", rec.Code, rec.Body)
	}
	n := network.Network{ID: "last", Name: "last", Labels: map[string]string{ownerLabel: ot.bobKey}}
	ot.fake.Networks = append(ot.fake.Networks, network.Summary{Network: n})
	before := len(ot.fake.Actions())
	if rec := create(); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "the most a user may have") {
		t.Errorf("create at the limit = %d %s, want 409", rec.Code, rec.Body)
	}
	if len(ot.fake.Actions()) != before {
		t.Errorf("actions = %+v, want no create", ot.fake.Actions()[before:])
	}
	// Admins have no limit.
	if rec := do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/networks"), cookie: ot.admin, body: `{"name":"web"}`}); rec.Code != http.StatusCreated {
		t.Errorf("admin create = %d %s", rec.Code, rec.Body)
	}
}

// Giving a user an environment creates their default network there, once.
func TestDefaultNetworkOnAccess(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	aliceID, err := ot.svc.OwnerID(t.Context(), ot.alice)
	if err != nil {
		t.Fatal(err)
	}
	set := func(envs string) {
		t.Helper()
		rec := do(t, ot.h, request{method: "PUT", path: fmt.Sprintf("/api/v1/users/%d/environments", aliceID), cookie: ot.admin,
			body: `{"environments":[` + envs + `]}`})
		if rec.Code != http.StatusOK {
			t.Fatalf("set environments = %d %s", rec.Code, rec.Body)
		}
	}
	set(fmt.Sprint(ot.local))
	actions := ot.fake.Actions()
	if len(actions) != 1 || actions[0].Name != "network-create" || actions[0].ID != defaultNetwork(aliceID) {
		t.Fatalf("actions = %+v, want the default network created", actions)
	}
	opts := actions[0].Options.(client.NetworkCreateOptions)
	if opts.Driver != "bridge" || !reflect.DeepEqual(opts.Labels, map[string]string{ownerLabel: ot.alice}) {
		t.Errorf("options = %+v, want a bridge labelled as alice's", opts)
	}
	// Only environments newly given create one.
	set(fmt.Sprint(ot.local))
	if n := len(ot.fake.Actions()); n != 1 {
		t.Errorf("actions after the same set = %d, want 1", n)
	}
}

func TestEnsureDefaultNetwork(t *testing.T) {
	t.Parallel()
	const key = "k"
	name := defaultNetwork(7)
	conflict := func(dockertest.Action) error { return cerrdefs.ErrConflict }
	for _, tt := range []struct {
		name    string
		labels  map[string]string // of an existing network of that name; nil for none
		fail    func(dockertest.Action) error
		created bool
		err     string
	}{
		{name: "missing", created: true},
		{name: "there", labels: map[string]string{ownerLabel: key}},
		{name: "leftover", labels: map[string]string{ownerLabel: "deleted-user"}, err: "isn't the user's"},
		{name: "admin's", labels: map[string]string{}, err: "isn't the user's"},
		{name: "create fails", fail: conflict, err: "not found"},
	} {
		fake := &dockertest.Fake{}
		fake.NetworkDetails = map[string]network.Inspect{}
		if tt.labels != nil {
			fake.NetworkDetails[name] = network.Inspect{Network: network.Network{Name: name, Labels: tt.labels}}
		}
		fake.FailAction = tt.fail
		err := ensureDefaultNetwork(context.Background(), fake, 7, key)
		if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.err)
		}
		if created := len(fake.Actions()) == 1; created != tt.created {
			t.Errorf("%s: actions = %+v, want created %v", tt.name, fake.Actions(), tt.created)
		}
	}
}
