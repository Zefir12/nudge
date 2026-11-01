package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/moby/moby/api/types/volume"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestVolumeSizes(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		Volumes:     []volume.Volume{{Name: "db", Driver: "local"}, {Name: "nfs", Driver: "nfs"}},
		VolumeUsage: map[string]volume.UsageData{"db": {Size: 4096, RefCount: 1}},
	}}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake, "down": {Resources: dockertest.Resources{
		DiskUsageErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")},
	}}})
	base := fmt.Sprintf("/api/v1/environments/%d/volumes", et.ids["local"])
	list := func() map[string]volumeResponse {
		t.Helper()
		rec := do(t, et.h, request{method: "GET", path: base, cookie: et.cookie})
		if rec.Code != http.StatusOK {
			t.Fatalf("list: code = %d (%s)", rec.Code, rec.Body)
		}
		var vols []volumeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &vols); err != nil {
			t.Fatal(err)
		}
		byName := map[string]volumeResponse{}
		for _, v := range vols {
			byName[v.Name] = v
		}
		return byName
	}

	if v := list()["db"]; v.Size != nil || v.SizeMeasuredAt != nil {
		t.Errorf("before a measurement: db = %+v, want no size", v)
	}
	if rec := do(t, et.h, request{method: "POST", path: base + "/usage", cookie: et.cookie}); rec.Code != http.StatusNoContent {
		t.Fatalf("measure: code = %d (%s)", rec.Code, rec.Body)
	}
	vols := list()
	if v := vols["db"]; v.Size == nil || *v.Size != 4096 || v.SizeMeasuredAt == nil {
		t.Errorf("db = %+v, want 4096 bytes with a measurement time", v)
	}
	if v := vols["nfs"]; v.Size != nil || v.SizeMeasuredAt == nil {
		t.Errorf("nfs = %+v, want no size but a measurement time", v)
	}
	rec := do(t, et.h, request{method: "GET", path: base + "/db", cookie: et.cookie})
	var one volumeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || one.Size == nil || *one.Size != 4096 {
		t.Errorf("get db: %s, want its size", rec.Body)
	}

	tests := []struct {
		name     string
		path     string
		noCookie bool
		wantCode int
	}{
		{name: "offline", path: fmt.Sprintf("/api/v1/environments/%d/volumes/usage", et.ids["down"]), wantCode: http.StatusServiceUnavailable},
		{name: "unknown environment", path: "/api/v1/environments/999/volumes/usage", wantCode: http.StatusNotFound},
		{name: "without session", path: base + "/usage", noCookie: true, wantCode: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := request{method: "POST", path: tt.path, cookie: et.cookie}
			if tt.noCookie {
				req.cookie = nil
			}
			if rec := do(t, et.h, req); rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
		})
	}
}

func TestVolumeSizesUnavailable(t *testing.T) {
	t.Parallel()
	et := newEnvTestOpts(t, map[string]*dockertest.Fake{"local": {}}, func(o *Options) { o.Usage = nil })
	path := fmt.Sprintf("/api/v1/environments/%d/volumes/usage", et.ids["local"])
	if rec := do(t, et.h, request{method: "POST", path: path, cookie: et.cookie}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503 (%s)", rec.Code, rec.Body)
	}
}
