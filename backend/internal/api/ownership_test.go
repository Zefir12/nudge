package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	mevents "github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"git.zefirlabs.net/zefir/nudge/backend/internal/allowances"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

// ownTest is an API with two environments, "local" (bob may use it) and "other" (he may
// not), an admin, and the users bob and alice. In local, bob owns the container, volume
// and network named "mine"; alice owns "alices", the admins "admins" (no owner label),
// and "forged" carries bob's owner key only because its image sets the owner label.
type ownTest struct {
	h             http.Handler
	svc           *auth.Service
	fake          *dockertest.Fake
	msgs          chan mevents.Message
	errs          chan error
	admin, bob    *http.Cookie
	bobID         int64
	bobKey, alice string
	local, other  int64
	srv           *httptest.Server
	// allowances works on the API's database; bob's allowance in local is roomy.
	allowances *allowances.Service
}

// ownedObjects are the names (and IDs) of the containers, volumes and networks in local.
var ownedObjects = []string{"mine", "alices", "admins", "forged"}

func newOwnTest(t *testing.T) ownTest {
	t.Helper()
	return newOwnTestOpts(t, nil)
}

// newOwnTestOpts is newOwnTest with mod changing the API's options.
func newOwnTestOpts(t *testing.T, mod func(*Options)) ownTest {
	t.Helper()
	msgs, errs := make(chan mevents.Message), make(chan error, 1)
	fake := &dockertest.Fake{EventsResult: client.EventsResult{Messages: msgs, Err: errs}}
	h, svc, db := newTestHandlerOpts(t, func(environments.Environment) (docker.Client, error) { return fake, nil }, mod)
	ctx := context.Background()
	if _, err := svc.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	ot := ownTest{h: h, svc: svc, fake: fake, msgs: msgs, errs: errs, allowances: allowances.New(allowances.Options{DB: db})}
	ot.admin = sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds}))
	ot.bob = addUser(t, h, ot.admin, "bob", auth.RoleUser)
	addUser(t, h, ot.admin, "alice", auth.RoleUser)
	ids := map[string]int64{}
	for _, a := range listUsers(t, h, ot.admin) {
		ids[a.Username] = a.ID
	}
	ot.bobID = ids["bob"]
	var err error
	if ot.bobKey, err = svc.OwnerKey(ctx, ids["bob"]); err != nil {
		t.Fatal(err)
	}
	if ot.alice, err = svc.OwnerKey(ctx, ids["alice"]); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"local", "other"} {
		res, err := db.ExecContext(ctx, `INSERT INTO environments (name, kind, created_at) VALUES (?, 'local', 0)`, name)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if name == "local" {
			ot.local = id
		} else {
			ot.other = id
		}
	}
	labels := map[string]map[string]string{
		"mine":   {ownerLabel: ot.bobKey, "app": "web"},
		"alices": {ownerLabel: ot.alice},
		"admins": {"app": "db"},
		"forged": {ownerLabel: ot.bobKey},
	}
	plain := image.InspectResponse{ID: "sha256:plain", Config: &dockerspec.DockerOCIImageConfig{}}
	fake.ImageDetails = map[string]image.InspectResponse{
		"sha256:plain": plain,
		"nginx:1":      plain,
		"sha256:forged": {ID: "sha256:forged", Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{
			Labels: map[string]string{ownerLabel: ot.bobKey},
		}}},
	}
	fake.ContainerDetails = map[string]container.InspectResponse{}
	fake.Logs = map[string][]byte{}
	fake.Stats = map[string][]container.StatsResponse{}
	fake.NetworkDetails = map[string]network.Inspect{}
	for _, name := range ownedObjects {
		img := "sha256:plain"
		if name == "forged" {
			img = "sha256:forged"
		}
		fake.Containers = append(fake.Containers, container.Summary{
			ID: name, Names: []string{"/" + name}, Image: "nginx:1", ImageID: img, State: container.StateRunning, Labels: labels[name],
		})
		fake.ContainerDetails[name] = container.InspectResponse{
			ID: name, Name: "/" + name, Image: img, State: &container.State{Running: true, Status: container.StateRunning},
			Config:     &container.Config{Image: "nginx:1", Labels: labels[name]},
			HostConfig: &container.HostConfig{Resources: container.Resources{NanoCPUs: 500_000_000, Memory: 256 << 20}},
		}
		fake.Logs[name] = muxed("stdout", name+" says hello\n")
		// Only a container's labels can come from an image: bob owns no "forged" volume or
		// network.
		objLabels := labels[name]
		if name == "forged" {
			objLabels = nil
		}
		fake.Volumes = append(fake.Volumes, volume.Volume{Name: name, Driver: "local", Labels: objLabels})
		n := network.Network{ID: name, Name: name, Driver: "bridge", Labels: objLabels}
		fake.Networks = append(fake.Networks, network.Summary{Network: n})
		fake.NetworkDetails[name] = network.Inspect{Network: n, Containers: map[string]network.EndpointResource{
			"mine": {Name: "mine"}, "admins": {Name: "admins"},
		}}
	}
	// Bob's default network is there already, so giving him local creates nothing.
	def := network.Network{ID: "default", Name: defaultNetwork(ot.bobID), Driver: "bridge", Labels: map[string]string{ownerLabel: ot.bobKey}}
	fake.NetworkDetails[def.Name] = network.Inspect{Network: def}
	rec := do(t, h, request{method: "PUT", path: fmt.Sprintf("/api/v1/users/%d/environments", ot.bobID), cookie: ot.admin,
		body: fmt.Sprintf(`{"environments":[%d]}`, ot.local)})
	if rec.Code != http.StatusOK {
		t.Fatalf("set environments = %d %s", rec.Code, rec.Body)
	}
	if _, err := ot.allowances.Set(ctx, ot.bobID, ot.local, allowances.Allowance{
		CPUsMilli: 4000, MemoryBytes: 8 << 30, DiskBytes: 100 << 30, MaxContainers: 10, PortMin: 8000, PortMax: 8999,
	}); err != nil {
		t.Fatal(err)
	}
	ot.srv = httptest.NewServer(h)
	t.Cleanup(ot.srv.Close)
	return ot
}

func (ot ownTest) path(env int64, rest string) string {
	return "/api/v1/environments/" + strconv.FormatInt(env, 10) + rest
}

// TestOwnershipRoutes walks every environment route open to users: in an environment
// bob may not use everything is 404; another user's, the admins' and a forged object is
// 404 too; his own works.
func TestOwnershipRoutes(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	var tested int
	for p := range userRoutes {
		method, path, _ := strings.Cut(p, " ")
		if !strings.Contains(path, "{env}") {
			continue
		}
		tested++
		withEnv := func(env int64, id string) string {
			s := strings.Replace(path, "{env}", strconv.FormatInt(env, 10), 1)
			return strings.Replace(s, "{id}", id, 1)
		}
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			if rec := do(t, ot.h, request{method: method, path: withEnv(ot.other, "mine"), cookie: ot.bob, body: `{"name":"x"}`}); rec.Code != http.StatusNotFound ||
				!strings.Contains(rec.Body.String(), "environment not found") {
				t.Errorf("other environment = %d %s, want 404", rec.Code, rec.Body)
			}
			if !strings.Contains(path, "{id}") {
				return
			}
			for _, id := range []string{"alices", "admins", "forged"} {
				before := len(ot.fake.Actions())
				rec := do(t, ot.h, request{method: method, path: withEnv(ot.local, id), cookie: ot.bob, body: "{}"})
				if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not found") {
					t.Errorf("%s = %d %s, want 404", id, rec.Code, rec.Body)
				}
				for _, a := range ot.fake.Actions()[before:] {
					if a.ID == id {
						t.Errorf("%s: action %s on it", id, a.Name)
					}
				}
			}
			// WebSocket routes answer a plain request after the ownership check.
			if rec := do(t, ot.h, request{method: method, path: withEnv(ot.local, "mine"), cookie: ot.bob, body: "{}"}); rec.Code == http.StatusNotFound || rec.Code >= 500 {
				t.Errorf("own = %d %s, want it to work", rec.Code, rec.Body)
			}
		})
	}
	if tested < 20 {
		t.Errorf("tested %d environment routes, want every one open to users", tested)
	}
}

func TestOwnershipLists(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	names := func(cookie *http.Cookie, path string) []string {
		t.Helper()
		rec := do(t, ot.h, request{method: "GET", path: path, cookie: cookie})
		var items []struct {
			Name string `json:"name"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &items) != nil {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.Name)
		}
		slices.Sort(out)
		return out
	}
	for _, kind := range []string{"containers", "volumes", "networks"} {
		if got := names(ot.bob, ot.path(ot.local, "/"+kind)); !reflect.DeepEqual(got, []string{"mine"}) {
			t.Errorf("bob's %s = %v, want only his own", kind, got)
		}
		if got := names(ot.admin, ot.path(ot.local, "/"+kind)); len(got) != len(ownedObjects) {
			t.Errorf("admin's %s = %v, want all", kind, got)
		}
	}
	if got := names(ot.bob, "/api/v1/environments"); !reflect.DeepEqual(got, []string{"local"}) {
		t.Errorf("bob's environments = %v, want only local", got)
	}
	if got := names(ot.admin, "/api/v1/environments"); len(got) != 2 {
		t.Errorf("admin's environments = %v, want both", got)
	}
	// His network's detail lists only his containers.
	rec := do(t, ot.h, request{method: "GET", path: ot.path(ot.local, "/networks/mine"), cookie: ot.bob})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"mine"`) || strings.Contains(rec.Body.String(), "admins") {
		t.Errorf("bob's network = %d %s, want only his container attached", rec.Code, rec.Body)
	}
	// A state filter keeps the owner's.
	if got := names(ot.bob, ot.path(ot.local, "/containers?state=running")); !reflect.DeepEqual(got, []string{"mine"}) {
		t.Errorf("bob's running containers = %v, want only his own", got)
	}

	// The environment's counts and usage are his own.
	ot.fake.InfoResult = client.SystemInfoResult{Info: system.Info{Containers: 4, ContainersRunning: 4, Images: 9}}
	rec = do(t, ot.h, request{method: "GET", path: ot.path(ot.local, ""), cookie: ot.bob})
	var env environmentDetailResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &env) != nil {
		t.Fatalf("environment = %d %s", rec.Code, rec.Body)
	}
	if d := env.Docker; d.Containers != 1 || d.ContainersRunning != 1 || d.Images != 0 {
		t.Errorf("bob's counts = %+v, want his one running container and no images", d)
	}
	rec = do(t, ot.h, request{method: "GET", path: ot.path(ot.local, "/stats"), cookie: ot.bob})
	var stats environmentStats
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &stats) != nil {
		t.Fatalf("stats = %d %s", rec.Code, rec.Body)
	}
	if len(stats.Containers)+stats.Skipped != 1 {
		t.Errorf("bob's overview = %+v, want only his container", stats)
	}
}

// Events of another owner's (or a forged) container, and of volumes and networks, never
// reach a user; an admin gets them all.
func TestOwnershipEvents(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	lt := logsTest{envTest: envTest{h: ot.h, cookie: ot.bob}, srv: ot.srv, fake: ot.fake}
	bob, err := lt.dial(ctx, t, ot.path(ot.local, "/events"))
	if err != nil {
		t.Fatal(err)
	}
	lt.cookie = ot.admin
	admin, err := lt.dial(ctx, t, ot.path(ot.local, "/events"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range ownedObjects {
		attrs := map[string]string{"name": name}
		maps.Copy(attrs, ot.fake.ContainerDetails[name].Config.Labels)
		ot.msgs <- mevents.Message{Type: "container", Action: "start", Time: 1_800_000_000, Actor: mevents.Actor{ID: name, Attributes: attrs}}
		ot.msgs <- mevents.Message{Type: "volume", Action: "mount", Time: 1_800_000_000, Actor: mevents.Actor{ID: name}}
	}
	// A container that is gone before the stream saw it can't be checked.
	ot.msgs <- mevents.Message{Type: "container", Action: "destroy", Time: 1_800_000_000, Actor: mevents.Actor{ID: "gone", Attributes: map[string]string{ownerLabel: ot.bobKey}}}
	ot.errs <- fmt.Errorf("daemon gone")

	ids := func(frames []map[string]any) []string {
		var out []string
		for _, f := range frames {
			if id, ok := f["id"].(string); ok {
				out = append(out, f["type"].(string)+":"+id)
			}
		}
		return out
	}
	if got := ids(readAll(ctx, t, bob)); !reflect.DeepEqual(got, []string{"container:mine"}) {
		t.Errorf("bob's events = %v, want only his container's", got)
	}
	if got := ids(readAll(ctx, t, admin)); len(got) != 2*len(ownedObjects)+1 {
		t.Errorf("admin's events = %v, want all", got)
	}
}

// A user's log stream of his own container works; anyone else's is refused before the
// upgrade (TestOwnershipRoutes), so no line of it is sent.
func TestOwnershipLogs(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	lt := logsTest{envTest: envTest{h: ot.h, cookie: ot.bob}, srv: ot.srv, fake: ot.fake}
	ws, err := lt.dial(ctx, t, ot.path(ot.local, "/containers/mine/logs"))
	if err != nil {
		t.Fatal(err)
	}
	frames := readAll(ctx, t, ws)
	if len(frames) == 0 || frames[0]["line"] != "mine says hello" {
		t.Errorf("frames = %v", frames)
	}
	if _, err := lt.dial(ctx, t, ot.path(ot.local, "/containers/admins/logs")); err == nil {
		t.Error("dial of the admins' logs worked")
	}
}

// Labels in nudge's namespace are refused on create; a recreate keeps the owner label,
// which the spec doesn't show.
func TestOwnerLabelReserved(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	for _, tt := range []struct{ path, body string }{
		{"/containers", `{"image":"nginx:1","labels":{"net.zefirlabs.nudge.owner":"x"}}`},
		{"/volumes", `{"name":"v","labels":{"Net.Zefirlabs.Nudge.Anything":"x"}}`},
		{"/networks", `{"name":"n","labels":{"net.zefirlabs.nudge.owner":"x"}}`},
	} {
		rec := do(t, ot.h, request{method: "POST", path: ot.path(ot.local, tt.path), cookie: ot.admin, body: tt.body})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reserved for nudge") {
			t.Errorf("%s = %d %s, want 400 reserved", tt.path, rec.Code, rec.Body)
		}
	}
	if len(ot.fake.Actions()) != 0 {
		t.Errorf("actions = %+v, want none", ot.fake.Actions())
	}

	// An admin's create sets no owner.
	rec := do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/volumes"), cookie: ot.admin, body: `{"name":"v","labels":{"app":"x"}}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if opts := ot.fake.Actions()[0].Options.(client.VolumeCreateOptions); !reflect.DeepEqual(opts.Labels, map[string]string{"app": "x"}) {
		t.Errorf("labels = %v, want the admin's own only", opts.Labels)
	}

	rec = do(t, ot.h, request{method: "GET", path: ot.path(ot.local, "/containers/mine/spec"), cookie: ot.admin})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), ownerLabel) {
		t.Errorf("spec = %d %s, want no owner label", rec.Code, rec.Body)
	}
	// A user's container is recreated through the tenant builder, an admin's request too.
	rec = do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/user-containers/mine/recreate"), cookie: ot.admin,
		body: `{"image":"nginx:1","cpus_milli":500,"memory_bytes":268435456,"labels":{"app":"api"}}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("recreate = %d %s", rec.Code, rec.Body)
	}
	if cfg := createOptions(t, ot.fake).Config; !reflect.DeepEqual(cfg.Labels, map[string]string{"app": "api", ownerLabel: ot.bobKey}) {
		t.Errorf("recreated labels = %v, want the spec's and the owner", cfg.Labels)
	}
}

// A user's object is labelled with their owner key, an admin's with none.
func TestOwnerLabels(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	s := &server{auth: ot.svc}
	in := map[string]string{"app": "web"}
	got, err := s.ownerLabels(withUser(t.Context(), auth.User{ID: ot.bobID, Role: auth.RoleUser}), in)
	if err != nil || !reflect.DeepEqual(got, map[string]string{"app": "web", ownerLabel: ot.bobKey}) {
		t.Errorf("user's labels = %v, %v", got, err)
	}
	if len(in) != 1 {
		t.Errorf("input changed: %v", in)
	}
	if got, err := s.ownerLabels(withUser(t.Context(), auth.User{ID: 1, Role: auth.RoleAdmin}), in); err != nil || !reflect.DeepEqual(got, in) {
		t.Errorf("admin's labels = %v, %v", got, err)
	}
}

func TestUserEnvironments(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	base := fmt.Sprintf("/api/v1/users/%d/environments", ot.bobID)
	for _, tt := range []struct {
		name, method, path, body string
		cookie                   *http.Cookie
		code                     int
		want                     string
	}{
		{"get", "GET", base, "", ot.admin, http.StatusOK, fmt.Sprintf(`{"environments":[%d]}`, ot.local)},
		{"set", "PUT", base, fmt.Sprintf(`{"environments":[%d,%d,%d]}`, ot.other, ot.local, ot.other), ot.admin, http.StatusOK,
			fmt.Sprintf(`{"environments":[%d,%d]}`, ot.local, ot.other)},
		{"unknown environment", "PUT", base, `{"environments":[999]}`, ot.admin, http.StatusBadRequest, `{"error":"no such environment"}`},
		{"no list", "PUT", base, `{}`, ot.admin, http.StatusBadRequest, `{"error":"environments must be a list of environment ids"}`},
		{"unknown user", "GET", "/api/v1/users/999/environments", "", ot.admin, http.StatusNotFound, `{"error":"user not found"}`},
		{"user", "PUT", base, `{"environments":[]}`, ot.bob, http.StatusForbidden, ""},
	} {
		rec := do(t, ot.h, request{method: tt.method, path: tt.path, body: tt.body, cookie: tt.cookie})
		if rec.Code != tt.code || tt.want != "" && strings.TrimSpace(rec.Body.String()) != tt.want {
			t.Errorf("%s = %d %s, want %d %s", tt.name, rec.Code, rec.Body, tt.code, tt.want)
		}
	}
	// The failed set changed nothing.
	rec := do(t, ot.h, request{method: "GET", path: base, cookie: ot.admin})
	if want := fmt.Sprintf(`{"environments":[%d,%d]}`, ot.local, ot.other); strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("after a failed set = %s, want %s", rec.Body, want)
	}
}
