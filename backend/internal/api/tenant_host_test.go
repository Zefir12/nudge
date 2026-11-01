package api

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

func TestBuildTenantHosting(t *testing.T) {
	t.Parallel()
	user := tenantOwner{key: "key7", id: 7, prefix: "u7-"}
	all := TenantHosting{CgroupParent: "nudge-u{id}.slice", Runtime: "runsc", StorageSize: 10 << 30}
	tests := []struct {
		name    string
		owner   tenantOwner
		host    TenantHosting
		cgroup  string
		runtime string
		storage map[string]string
	}{
		{name: "unset", owner: user},
		{name: "cgroup parent", owner: user, host: TenantHosting{CgroupParent: "nudge-u{id}.slice"}, cgroup: "nudge-u7.slice"},
		{name: "cgroupfs path", owner: user, host: TenantHosting{CgroupParent: "/nudge/u{id}/{id}"}, cgroup: "/nudge/u7/7"},
		{name: "runtime", owner: user, host: TenantHosting{Runtime: "runsc"}, runtime: "runsc"},
		{name: "storage size", owner: user, host: TenantHosting{StorageSize: 10 << 30}, storage: map[string]string{"size": "10737418240"}},
		{name: "all", owner: user, host: all, cgroup: "nudge-u7.slice", runtime: "runsc", storage: map[string]string{"size": "10737418240"}},
		// An admin's container built from a spec gets none of it.
		{name: "admin's", host: all},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sp := tenantSpec{Image: "nginx:1", Name: "web", CPUsMilli: 500, MemoryBytes: 256 << 20}
			_, hc, _ := buildTenant(&sp, tt.owner, tt.host)
			if hc.CgroupParent != tt.cgroup || hc.Runtime != tt.runtime || !reflect.DeepEqual(hc.StorageOpt, tt.storage) {
				t.Errorf("cgroup parent %q, runtime %q, storage %v; want %q, %q, %v", hc.CgroupParent, hc.Runtime, hc.StorageOpt, tt.cgroup, tt.runtime, tt.storage)
			}
			// The rest of the hardening stays.
			if hc.PidsLimit == nil || len(hc.CapDrop) != 1 || hc.Privileged {
				t.Errorf("host config lost its hardening: %+v", hc)
			}
		})
	}
}

func TestStorageSizeSupported(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		info system.Info
		want bool
	}{
		{"overlay2 on xfs", system.Info{Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}, {"Supports d_type", "true"}}}, true},
		{"overlay2 on ext4", system.Info{Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "extfs"}}}, false},
		{"overlay2 without status", system.Info{Driver: "overlay2"}, false},
		{"btrfs", system.Info{Driver: "btrfs"}, true},
		{"zfs", system.Info{Driver: "zfs"}, true},
		{"containerd snapshotter", system.Info{Driver: "overlayfs", DriverStatus: [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}}, false},
		{"vfs", system.Info{Driver: "vfs"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := storageSizeSupported(tt.info); got != tt.want {
				t.Errorf("storageSizeSupported = %v, want %v", got, tt.want)
			}
		})
	}
}

// hostedInfo is a daemon with gVisor on overlay2 over xfs.
var hostedInfo = system.Info{
	Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}},
	Runtimes: map[string]system.RuntimeWithStatus{"runc": {}, "runsc": {}},
}

func withHosting(o *Options) {
	o.Tenant = TenantHosting{CgroupParent: "nudge-u{id}.slice", Runtime: "runsc", StorageSize: 1 << 30}
}

func TestCreateTenantHosting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		info    system.Info
		cookie  func(ot ownTest) *http.Cookie
		code    int
		runtime string
		storage bool
	}{
		{name: "hardened", info: hostedInfo, cookie: func(ot ownTest) *http.Cookie { return ot.bob }, code: http.StatusCreated, runtime: "runsc", storage: true},
		{name: "no disk cap on ext4", info: system.Info{Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "extfs"}}, Runtimes: hostedInfo.Runtimes},
			cookie: func(ot ownTest) *http.Cookie { return ot.bob }, code: http.StatusCreated, runtime: "runsc"},
		{name: "daemon without the runtime", info: system.Info{Driver: "overlay2", Runtimes: map[string]system.RuntimeWithStatus{"runc": {}}},
			cookie: func(ot ownTest) *http.Cookie { return ot.bob }, code: http.StatusConflict},
		// An admin's container from a spec is the admin's: no hardening, no check.
		{name: "admin's", info: system.Info{}, cookie: func(ot ownTest) *http.Cookie { return ot.admin }, code: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ot := newTenantTestOpts(t, withHosting)
			ot.fake.InfoResult = client.SystemInfoResult{Info: tt.info}
			before := len(ot.fake.Actions())
			rec := do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/user-containers"), cookie: tt.cookie(ot), body: `{` + tenantBase + `}`})
			if rec.Code != tt.code {
				t.Fatalf("create = %d %s, want %d", rec.Code, rec.Body, tt.code)
			}
			cs := created(ot.fake, before)
			if tt.code != http.StatusCreated {
				if len(cs) > 0 || !strings.Contains(rec.Body.String(), `no runtime \"runsc\"`) {
					t.Errorf("created %d containers, answer %s", len(cs), rec.Body)
				}
				return
			}
			hc := cs[0].HostConfig
			wantCgroup := ""
			if tt.runtime != "" {
				wantCgroup = strings.ReplaceAll("nudge-u{id}.slice", "{id}", strconv.FormatInt(ot.bobID, 10))
			}
			if hc.Runtime != tt.runtime || hc.CgroupParent != wantCgroup || (hc.StorageOpt["size"] == "1073741824") != tt.storage {
				t.Errorf("runtime %q, cgroup parent %q, storage %v", hc.Runtime, hc.CgroupParent, hc.StorageOpt)
			}
		})
	}
}

func TestTenantHostingReplaceAndStart(t *testing.T) {
	t.Parallel()
	missing := system.Info{Runtimes: map[string]system.RuntimeWithStatus{"runc": {}}}
	ext4 := system.Info{Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "extfs"}}, Runtimes: hostedInfo.Runtimes}
	// hosted is how "mine" (bob's) was made with withHosting's settings; {id} is bob's.
	hosted := made{runtime: "runsc", cgroup: "nudge-u{id}.slice", size: "1073741824"}
	tests := []struct {
		name string
		info system.Info
		// mine is how "mine" was made.
		mine   made
		method string
		path   string
		admin  bool
		body   string
		code   int
		// refusal is part of a 409's message.
		refusal string
	}{
		{name: "recreate builds with the runtime", info: hostedInfo, path: "/user-containers/mine/recreate", body: `{` + tenantBase + `}`, code: http.StatusOK},
		{name: "update builds with the runtime", info: hostedInfo, path: "/containers/mine/update", code: http.StatusOK},
		{name: "recreate without the runtime on the daemon", info: missing, path: "/user-containers/mine/recreate", body: `{` + tenantBase + `}`, code: http.StatusConflict},
		{name: "start with the runtime", info: hostedInfo, mine: hosted, path: "/containers/mine/start", code: http.StatusNoContent},
		{name: "restart with the runtime", info: hostedInfo, mine: hosted, path: "/containers/mine/restart", code: http.StatusNoContent},
		{name: "start made before the runtime", info: hostedInfo, mine: made{cgroup: hosted.cgroup, size: hosted.size}, path: "/containers/mine/start", code: http.StatusConflict, refusal: `runtime \"runsc\"`},
		{name: "start without the runtime on the daemon", info: missing, mine: hosted, path: "/containers/mine/start", code: http.StatusConflict, refusal: `no runtime`},
		{name: "start made before the cgroup parent", info: hostedInfo, mine: made{runtime: "runsc", size: hosted.size}, path: "/containers/mine/start", code: http.StatusConflict, refusal: "cgroup parent"},
		{name: "restart with a stale cgroup parent", info: hostedInfo, mine: made{runtime: "runsc", cgroup: "old-u{id}.slice", size: hosted.size}, path: "/containers/mine/restart", code: http.StatusConflict, refusal: "cgroup parent"},
		{name: "start made before the disk cap", info: hostedInfo, mine: made{runtime: "runsc", cgroup: hosted.cgroup}, path: "/containers/mine/start", code: http.StatusConflict, refusal: "disk cap"},
		{name: "start with another disk cap", info: hostedInfo, mine: made{runtime: "runsc", cgroup: hosted.cgroup, size: "2147483648"}, path: "/containers/mine/start", code: http.StatusConflict, refusal: "disk cap"},
		// A driver that can't cap a container's disk made it without one: it starts.
		{name: "start without a disk cap on ext4", info: ext4, mine: made{runtime: "runsc", cgroup: hosted.cgroup}, path: "/containers/mine/start", code: http.StatusNoContent},
		{name: "admin's container starts as it is", info: missing, path: "/containers/admins/start", admin: true, code: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ot := newTenantTestOpts(t, withHosting)
			ot.fake.InfoResult = client.SystemInfoResult{Info: tt.info}
			ct := ot.fake.ContainerDetails["mine"]
			ct.HostConfig.Runtime = tt.mine.runtime
			ct.HostConfig.CgroupParent = strings.ReplaceAll(tt.mine.cgroup, "{id}", strconv.FormatInt(ot.bobID, 10))
			ct.HostConfig.StorageOpt = nil
			if tt.mine.size != "" {
				ct.HostConfig.StorageOpt = map[string]string{"size": tt.mine.size}
			}
			ot.fake.ContainerDetails["mine"] = ct
			// A newer image for the update.
			ot.fake.ImageDetails["nginx:1"] = image.InspectResponse{ID: "sha256:newer", Config: &dockerspec.DockerOCIImageConfig{}}
			cookie := ot.bob
			if tt.admin {
				cookie = ot.admin
			}
			before := len(ot.fake.Actions())
			rec := do(t, ot.h, request{method: "POST", path: ot.path(ot.local, tt.path), cookie: cookie, body: tt.body})
			if rec.Code != tt.code {
				t.Fatalf("%s = %d %s, want %d", tt.path, rec.Code, rec.Body, tt.code)
			}
			cs := created(ot.fake, before)
			if tt.code == http.StatusOK {
				if len(cs) != 1 || cs[0].HostConfig.Runtime != "runsc" || cs[0].HostConfig.CgroupParent == "" {
					t.Errorf("created %+v, want one with the runtime and slice", cs)
				}
			}
			if tt.code == http.StatusConflict {
				if !strings.Contains(rec.Body.String(), tt.refusal) {
					t.Errorf("answer %s, want it to name %s", rec.Body, tt.refusal)
				}
				for _, a := range ot.fake.Actions()[before:] {
					if a.Name == "start" || a.Name == "create" {
						t.Errorf("refused, yet %s", a.Name)
					}
				}
			}
		})
	}
}

// made is how a user's container was made: its runtime, cgroup parent ({id} is the
// owner's) and disk cap.
type made struct {
	runtime, cgroup, size string
}
