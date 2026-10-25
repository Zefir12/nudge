package registries

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	return New(Options{DB: st.DB(), Box: box, Now: func() time.Time { return now }})
}

func ptr(s string) *string { return &s }

func TestHostOf(t *testing.T) {
	t.Parallel()
	tests := []struct{ ref, want string }{
		{"nginx", DockerHub},
		{"nginx:1.27", DockerHub},
		{"library/nginx:latest", DockerHub},
		{"zefir/app", DockerHub},
		{"docker.io/library/nginx", DockerHub},
		{"index.docker.io/zefir/app", DockerHub},
		{"ghcr.io/org/app:1.2", "ghcr.io"},
		{"GHCR.io/org/app", "ghcr.io"},
		{"localhost/app", "localhost"},
		{"localhost:5000/app", "localhost:5000"},
		{"registry.example.com:5000/team/app@sha256:abc", "registry.example.com:5000"},
		{"app@sha256:abc", DockerHub},
	}
	for _, tt := range tests {
		if got := HostOf(tt.ref); got != tt.want {
			t.Errorf("HostOf(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "ghcr.io", want: "ghcr.io"},
		{in: " https://GHCR.io/ ", want: "ghcr.io"},
		{in: "https://index.docker.io/v1/", want: DockerHub},
		{in: "registry-1.docker.io", want: DockerHub},
		{in: "http://registry.local:5000/v2", want: "registry.local:5000"},
		{in: "", wantErr: true},
		{in: "https://", wantErr: true},
		{in: "bad host", wantErr: true},
		{in: "user:pass@ghcr.io", wantErr: true},
		{in: "-ghcr.io", wantErr: true},
		{in: "ghcr.io:port", wantErr: true},
	}
	for _, tt := range tests {
		got, err := NormalizeAddress(tt.in)
		var inputErr *InputError
		if tt.wantErr {
			if !errors.As(err, &inputErr) {
				t.Errorf("NormalizeAddress(%q) err = %v, want InputError", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("NormalizeAddress(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestCreateValidation(t *testing.T) {
	t.Parallel()
	s := newTestService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, Input{Name: "hub", Address: "docker.io", Username: "u", Password: ptr("p")}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   Input
		want error
	}{
		{name: "no name", in: Input{Address: "ghcr.io", Username: "u", Password: ptr("p")}},
		{name: "spaces", in: Input{Name: " x", Address: "ghcr.io", Username: "u", Password: ptr("p")}},
		{name: "no username", in: Input{Name: "x", Address: "ghcr.io", Password: ptr("p")}},
		{name: "no password", in: Input{Name: "x", Address: "ghcr.io", Username: "u"}},
		{name: "empty password", in: Input{Name: "x", Address: "ghcr.io", Username: "u", Password: ptr("")}},
		{name: "bad address", in: Input{Name: "x", Address: "not a host", Username: "u", Password: ptr("p")}},
		{name: "bad kind", in: Input{Name: "x", Address: "ghcr.io", Username: "u", Password: ptr("p"), Kind: "zot"}},
		{name: "owners on generic", in: Input{Name: "x", Address: "ghcr.io", Username: "u", Password: ptr("p"), Owners: []string{"a"}}},
		{name: "owner with slash", in: Input{Name: "x", Address: "ghcr.io", Username: "u", Password: ptr("p"), Kind: Forgejo, Owners: []string{"a/b"}}},
		{name: "owner with dots", in: Input{Name: "x", Address: "ghcr.io", Username: "u", Password: ptr("p"), Kind: Forgejo, Owners: []string{"a..b"}}},
		{name: "owner with comma", in: Input{Name: "x", Address: "ghcr.io", Username: "u", Password: ptr("p"), Kind: Forgejo, Owners: []string{"a,b"}}},
		{name: "too many owners", in: Input{Name: "x", Address: "ghcr.io", Username: "u", Password: ptr("p"), Kind: Forgejo,
			Owners: []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11", "a12", "a13", "a14", "a15", "a16", "a17", "a18", "a19", "a20", "a21"}}},
		{name: "name taken", in: Input{Name: "hub", Address: "ghcr.io", Username: "u", Password: ptr("p")}, want: ErrTaken},
		{name: "address taken", in: Input{Name: "x", Address: "https://index.docker.io/v1/", Username: "u", Password: ptr("p")}, want: ErrTaken},
	}
	for _, tt := range tests {
		_, err := s.Create(ctx, tt.in)
		if tt.want != nil {
			if !errors.Is(err, tt.want) {
				t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
			}
			continue
		}
		var inputErr *InputError
		if !errors.As(err, &inputErr) {
			t.Errorf("%s: err = %v, want InputError", tt.name, err)
		}
	}
}

func TestUpdateNeedsPasswordForNewTarget(t *testing.T) {
	t.Parallel()
	base := Input{Name: "Lan", Address: "registry.lan:5000", Username: "bot", Kind: Forgejo, Owners: []string{"zefir"}}
	tests := []struct {
		name    string
		change  func(in *Input)
		wantErr bool
	}{
		{name: "nothing changes", change: func(*Input) {}},
		{name: "name changes", change: func(in *Input) { in.Name = "Other" }},
		{name: "kind and owners change", change: func(in *Input) { in.Kind, in.Owners = Generic, nil }},
		{name: "same address, other spelling", change: func(in *Input) { in.Address = "https://Registry.LAN:5000/" }},
		{name: "address changes", change: func(in *Input) { in.Address = "evil.example.com" }, wantErr: true},
		{name: "username changes", change: func(in *Input) { in.Username = "bot2" }, wantErr: true},
		{name: "plain_http changes", change: func(in *Input) { in.PlainHTTP = false }, wantErr: true},
		{name: "address changes with a password", change: func(in *Input) { in.Address, in.Password = "evil.example.com", ptr("new") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newTestService(t)
			ctx := context.Background()
			create := base
			create.Password, create.PlainHTTP = ptr("s3cret"), true
			r, err := s.Create(ctx, create)
			if err != nil {
				t.Fatal(err)
			}
			in := base
			in.PlainHTTP = true
			tt.change(&in)
			_, err = s.Update(ctx, r.ID, in)
			var inputErr *InputError
			if tt.wantErr {
				if !errors.As(err, &inputErr) || inputErr.Msg != "password required when address, username or plain_http changes" {
					t.Fatalf("err = %v, want the password-required InputError", err)
				}
				if got, _, _ := s.Credentials(ctx, r.ID); got.Address != "registry.lan:5000" || got.Username != "bot" || !got.PlainHTTP {
					t.Errorf("refused update changed the registry: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestKindAndCredentials(t *testing.T) {
	t.Parallel()
	s := newTestService(t)
	ctx := context.Background()
	r, err := s.Create(ctx, Input{Name: "Forge", Address: "git.example.com", Username: "bot", Password: ptr("tok"),
		Kind: Forgejo, Owners: []string{"zefir", "Team", "team", "zefir"}, PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != Forgejo || !r.PlainHTTP || !slices.Equal(r.Owners, []string{"zefir", "Team"}) {
		t.Errorf("created %+v", r)
	}
	got, password, err := s.Credentials(ctx, r.ID)
	if err != nil || password != "tok" || got.Address != "git.example.com" || got.Kind != Forgejo {
		t.Errorf("Credentials = %+v, %q, %v", got, password, err)
	}
	if _, _, err := s.Credentials(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Credentials(missing) err = %v", err)
	}
	// A PUT without a kind makes the registry generic again.
	r, err = s.Update(ctx, r.ID, Input{Name: "Forge", Address: "git.example.com", Username: "bot", Password: ptr("tok")})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != Generic || r.PlainHTTP || r.Owners == nil || len(r.Owners) != 0 {
		t.Errorf("updated %+v", r)
	}
}

func TestCRUDAndAuth(t *testing.T) {
	t.Parallel()
	s := newTestService(t)
	ctx := context.Background()
	if _, ok, err := s.AuthFor(ctx, "ghcr.io/org/app"); ok || err != nil {
		t.Fatalf("AuthFor with no registries = %v, %v", ok, err)
	}
	r, err := s.Create(ctx, Input{Name: "GitHub", Address: "https://ghcr.io/", Username: "bot", Password: ptr("s3cret")})
	if err != nil {
		t.Fatal(err)
	}
	if r.Address != "ghcr.io" || r.Username != "bot" || r.CreatedAt.Unix() != 1_700_000_000 {
		t.Errorf("created %+v", r)
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_enc FROM registries WHERE id = ?`, r.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("s3cret")) {
		t.Error("password stored in plain text")
	}
	a, ok, err := s.AuthFor(ctx, "ghcr.io/org/app:1.2")
	if err != nil || !ok || a != (Auth{Address: "ghcr.io", Username: "bot", Password: "s3cret"}) {
		t.Errorf("AuthFor = %+v, %v, %v", a, ok, err)
	}
	if _, ok, _ := s.AuthFor(ctx, "nginx"); ok {
		t.Error("AuthFor(nginx) found the ghcr.io credentials")
	}

	// Update without a password keeps it.
	if _, err := s.Update(ctx, r.ID, Input{Name: "GitHub", Address: "ghcr.io", Username: "bot"}); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := s.AuthFor(ctx, "ghcr.io/x"); a.Username != "bot" || a.Password != "s3cret" {
		t.Errorf("after update without password: %+v", a)
	}
	if _, err := s.Update(ctx, r.ID, Input{Name: "GitHub", Address: "ghcr.io", Username: "bot2", Password: ptr("new")}); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := s.AuthFor(ctx, "ghcr.io/x"); a.Password != "new" {
		t.Errorf("after password change: %+v", a)
	}

	other, err := s.Create(ctx, Input{Name: "Hub", Address: "docker.io", Username: "u", Password: ptr("p")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, other.ID, Input{Name: "GitHub", Address: "docker.io", Username: "u"}); !errors.Is(err, ErrTaken) {
		t.Errorf("rename to a taken name: %v", err)
	}
	if _, err := s.Update(ctx, 999, Input{Name: "x", Address: "x.io", Username: "u"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "GitHub" || list[1].Name != "Hub" {
		t.Errorf("List = %+v, %v", list, err)
	}
	if err := s.Delete(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	if _, err := s.Get(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}

func TestNamesHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text, host string
		want       bool
	}{
		{"image: GHCR.io/app", "ghcr.io", true},
		{"image: ghcr.io/app", "GHCR.IO", true},
		{"image: ghcr.io:443/app", "ghcr.io", false},
		{"image: my-ghcr.io/app", "ghcr.io", false},
		{"ghcr.io", "ghcr.io", true},
		{"", "ghcr.io", false},
	}
	for _, tt := range tests {
		if got := namesHost(tt.text, tt.host); got != tt.want {
			t.Errorf("namesHost(%q, %q) = %v, want %v", tt.text, tt.host, got, tt.want)
		}
	}
}

func TestAuthsFor(t *testing.T) {
	t.Parallel()
	s := newTestService(t)
	ctx := context.Background()
	if got, err := s.AuthsFor(ctx, "image: ghcr.io/org/app"); err != nil || len(got) != 0 {
		t.Fatalf("AuthsFor with no registries = %+v, %v", got, err)
	}
	for _, in := range []Input{
		{Name: "GitHub", Address: "ghcr.io", Username: "bot", Password: ptr("gh-pass")},
		{Name: "Hub", Address: "docker.io", Username: "hub", Password: ptr("hub-pass")},
		{Name: "Own", Address: "registry.example.com:5000", Username: "me", Password: ptr("own-pass")},
		{Name: "Local", Address: "localhost", Username: "lo", Password: ptr("lo-pass")},
	} {
		if _, err := s.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	hub := Auth{Address: "docker.io", Username: "hub", Password: "hub-pass"}
	gh := Auth{Address: "ghcr.io", Username: "bot", Password: "gh-pass"}
	own := Auth{Address: "registry.example.com:5000", Username: "me", Password: "own-pass"}
	lo := Auth{Address: "localhost", Username: "lo", Password: "lo-pass"}
	tests := []struct {
		name  string
		texts []string
		want  []Auth
	}{
		{"nothing named", []string{"services: {}\n"}, []Auth{hub}},
		{"in compose", []string{"services:\n  app:\n    image: ghcr.io/org/app:1\n"}, []Auth{hub, gh}},
		{"case and env", []string{"image: ${IMAGE}\n", "IMAGE=GHCR.IO/org/app\n"}, []Auth{hub, gh}},
		{"host with port", []string{"image: registry.example.com:5000/app"}, []Auth{hub, own}},
		{"other port", []string{"image: registry.example.com:5001/app"}, []Auth{hub}},
		{"no port named", []string{"image: registry.example.com/app"}, []Auth{hub}},
		{"longer host", []string{"image: notghcr.io/app\nimage: ghcr.io.example.com/app\nimage: localhost:5000/x"}, []Auth{hub}},
		{"quoted", []string{`image: "localhost/app"`}, []Auth{hub, lo}},
		{"several", []string{"image: ghcr.io/a\n", "B=registry.example.com:5000/b"}, []Auth{hub, gh, own}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.AuthsFor(ctx, tt.texts...)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("AuthsFor(%q) = %+v, want %+v", tt.texts, got, tt.want)
			}
		})
	}
}
