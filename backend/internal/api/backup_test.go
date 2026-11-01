package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/backup"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

func TestCreateBackup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	data := t.TempDir()
	st, err := store.Open(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := secretbox.LoadKey(data); err != nil {
		t.Fatal(err)
	}
	backups := backup.New(backup.Options{Store: st, DataDir: data, StacksDir: filepath.Join(data, "stacks"), Version: "v9"})
	h, svc, _ := newTestHandlerOpts(t, func(environments.Environment) (docker.Client, error) { return &dockertest.Fake{}, nil },
		func(o *Options) { o.Backups = backups })
	if _, err := svc.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	admin := sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds}))
	bob := addUser(t, h, admin, "bob", auth.RoleUser)
	rec := do(t, h, request{method: "POST", path: "/api/v1/tokens", cookie: admin, body: `{"name":"ci"}`})
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.Token == "" {
		t.Fatalf("token: %s", rec.Body)
	}
	const body = `{"passphrase":"correct horse battery"}`

	tests := []struct {
		name     string
		req      request
		wantCode int
	}{
		{"no session", request{body: body}, http.StatusUnauthorized},
		{"user", request{cookie: bob, body: body}, http.StatusForbidden},
		{"admin's API token", request{headers: map[string]string{"Authorization": "Bearer " + tok.Token}, body: body}, http.StatusForbidden},
		{"short passphrase", request{cookie: admin, body: `{"passphrase":"short"}`}, http.StatusBadRequest},
		{"no passphrase", request{cookie: admin, body: `{}`}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.method, tt.req.path = "POST", "/api/v1/backup"
			if rec := do(t, h, tt.req); rec.Code != tt.wantCode {
				t.Fatalf("code = %d %s, want %d", rec.Code, rec.Body, tt.wantCode)
			}
		})
	}

	rec = do(t, h, request{method: "POST", path: "/api/v1/backup", cookie: admin, body: body})
	if rec.Code != http.StatusOK {
		t.Fatalf("backup = %d %s", rec.Code, rec.Body)
	}
	if cd := rec.Header().Get("Content-Disposition"); !bytes.Contains([]byte(cd), []byte(`.nudgebak"`)) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	dst := t.TempDir()
	m, _, err := backup.Restore(ctx, bytes.NewReader(rec.Body.Bytes()), backup.RestoreOptions{
		DataDir: dst, StacksDir: filepath.Join(dst, "stacks"), Passphrase: "correct horse battery",
	})
	if err != nil {
		t.Fatalf("restore the download: %v", err)
	}
	if m.Version != "v9" {
		t.Errorf("manifest = %+v", m)
	}

	rec = do(t, h, request{method: "GET", path: "/api/v1/audit", cookie: admin})
	var audit struct {
		Entries []struct {
			Action string `json:"action"`
			Target string `json:"target"`
			Status int    `json:"status"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &audit); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range audit.Entries {
		if e.Action == "POST /api/v1/backup" && e.Status == http.StatusOK && e.Target == backup.FileName(m.Created) {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for the backup in %s", rec.Body)
	}
}
