package deployments

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// fakeClock moves only by Advance; After answers the channel the test fires.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	after chan time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) After(time.Duration) <-chan time.Time { return c.after }

type depTest struct {
	s     *Service
	clock *fakeClock
	env   int64
	// users by name: alice owns "web", bob is another user, root an admin.
	alice, bob, root Actor
}

func newDepTest(t *testing.T) depTest {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dt := depTest{clock: &fakeClock{now: time.UnixMilli(1_700_000_000_000), after: make(chan time.Time, 1)}}
	ids := map[string]int64{}
	for _, name := range []string{"alice", "bob", "root"} {
		res, err := st.DB().ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, 'x', 0, 0)`, name)
		if err != nil {
			t.Fatal(err)
		}
		if ids[name], err = res.LastInsertId(); err != nil {
			t.Fatal(err)
		}
	}
	res, err := st.DB().ExecContext(ctx, `INSERT INTO environments (name, kind, created_at) VALUES ('local', 'local', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	if dt.env, err = res.LastInsertId(); err != nil {
		t.Fatal(err)
	}
	dt.alice = Actor{UserID: ids["alice"], Username: "alice"}
	dt.bob = Actor{UserID: ids["bob"], Username: "bob"}
	dt.root = Actor{UserID: ids["root"], Username: "root", Admin: true}
	dt.s = New(Options{DB: st.DB(), Clock: dt.clock})
	t.Cleanup(dt.s.Close)
	return dt
}

func (dt depTest) create(t *testing.T, name string, vars ...string) Deployment {
	t.Helper()
	d, err := dt.s.Create(context.Background(), dt.alice, Input{Name: name, Environment: dt.env, Stack: "web", Variables: vars})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRoleAllows(t *testing.T) {
	t.Parallel()
	actions := []Action{ActionView, ActionDeploy, ActionEdit, ActionManageTokens, ActionManageMembers, ActionDelete}
	want := map[Role][]Action{
		RoleViewer:     {ActionView},
		RoleDeployer:   {ActionView, ActionDeploy},
		RoleMaintainer: {ActionView, ActionDeploy, ActionEdit, ActionManageTokens},
		RoleOwner:      actions,
		"":             nil,
		"root":         nil,
	}
	for role, allowed := range want {
		for _, a := range actions {
			wantOK := false
			for _, w := range allowed {
				wantOK = wantOK || w == a
			}
			if got := role.Allows(a); got != wantOK {
				t.Errorf("%q.Allows(%s) = %v, want %v", role, a, got, wantOK)
			}
		}
	}
	if RoleViewer.Allows("fly") {
		t.Error("an unknown action is allowed")
	}
}

func TestCreateAndAuthorize(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	d := dt.create(t, "web", "API_IMAGE", "API_IMAGE", "WORKER_IMAGE")
	if d.Role != RoleOwner || d.Stack != "web" || fmt.Sprint(d.Variables) != "[API_IMAGE WORKER_IMAGE]" {
		t.Errorf("created = %+v, want alice as owner, variables without repeats", d)
	}
	if _, err := dt.s.Create(ctx, dt.bob, Input{Name: "web", Environment: dt.env, Stack: "other"}); !errors.Is(err, ErrTaken) {
		t.Errorf("same name: err = %v, want ErrTaken", err)
	}

	invalid := []Input{
		{Name: "Web", Environment: dt.env, Stack: "web"},
		{Name: "-web", Environment: dt.env, Stack: "web"},
		{Name: "api", Stack: "web"},
		{Name: "api", Environment: dt.env, Stack: "../x"},
		{Name: "api", Environment: dt.env, Stack: "web", Variables: []string{"lower"}},
		{Name: "api", Environment: dt.env, Stack: "web", Variables: []string{"1ST"}},
	}
	for _, in := range invalid {
		var inputErr *InputError
		if _, err := dt.s.Create(ctx, dt.alice, in); !errors.As(err, &inputErr) {
			t.Errorf("Create(%+v): err = %v, want an InputError", in, err)
		}
	}

	// bob isn't a member: the deployment doesn't exist for him.
	if _, err := dt.s.Authorize(ctx, dt.bob, "web", ActionView); !errors.Is(err, ErrNotFound) {
		t.Errorf("non-member view: err = %v, want ErrNotFound", err)
	}
	if got, err := dt.s.List(ctx, dt.bob, 0); err != nil || len(got) != 0 {
		t.Errorf("non-member List = %v, %v; want none", got, err)
	}
	if got, err := dt.s.Authorize(ctx, dt.root, "web", ActionDelete); err != nil || got.Role != "" {
		t.Errorf("admin: %+v, %v; want allowed without a role", got, err)
	}
	if got, err := dt.s.List(ctx, dt.root, dt.env); err != nil || len(got) != 1 {
		t.Errorf("admin List = %v, %v; want web", got, err)
	}
	if got, err := dt.s.List(ctx, dt.root, dt.env+1); err != nil || len(got) != 0 {
		t.Errorf("admin List of another environment = %v, %v; want none", got, err)
	}

	// As a viewer bob sees it but can't deploy.
	if _, err := dt.s.SetMember(ctx, dt.alice, "web", "bob", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := dt.s.Authorize(ctx, dt.bob, "web", ActionView); err != nil {
		t.Errorf("viewer view: err = %v", err)
	}
	if _, err := dt.s.Authorize(ctx, dt.bob, "web", ActionDeploy); !errors.Is(err, ErrForbidden) {
		t.Errorf("viewer deploy: err = %v, want ErrForbidden", err)
	}
	if _, err := dt.s.Change(ctx, dt.bob, "web", Update{Description: new("mine")}); !errors.Is(err, ErrForbidden) {
		t.Errorf("viewer edit: err = %v, want ErrForbidden", err)
	}
	if _, err := dt.s.Authorize(ctx, dt.alice, "../web", ActionView); !errors.Is(err, ErrNotFound) {
		t.Errorf("invalid name: err = %v, want ErrNotFound", err)
	}
}

func TestChangeAndDelete(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	dt.create(t, "web", "API_IMAGE")
	dt.clock.Advance(time.Second)
	d, err := dt.s.Change(ctx, dt.alice, "web", Update{Description: new("the shop"), SetVariables: true, Variables: []string{"TAG"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Description != "the shop" || fmt.Sprint(d.Variables) != "[TAG]" || !d.UpdatedAt.After(d.CreatedAt) {
		t.Errorf("changed = %+v", d)
	}
	if d, err = dt.s.Change(ctx, dt.alice, "web", Update{}); err != nil || d.Description != "the shop" {
		t.Errorf("empty change = %+v, %v; want it unchanged", d, err)
	}
	if _, err := dt.s.Change(ctx, dt.alice, "web", Update{SetVariables: true, Variables: []string{"bad name"}}); err == nil {
		t.Error("invalid variable: err = nil")
	}
	if err := dt.s.Delete(ctx, dt.bob, "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("non-member delete: err = %v, want ErrNotFound", err)
	}
	if err := dt.s.Delete(ctx, dt.alice, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := dt.s.Authorize(ctx, dt.alice, "web", ActionView); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: err = %v, want ErrNotFound", err)
	}
}

func TestMembers(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	dt.create(t, "web")
	if _, err := dt.s.SetMember(ctx, dt.alice, "web", "nobody", RoleViewer); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("unknown user: err = %v, want ErrUnknownUser", err)
	}
	if _, err := dt.s.SetMember(ctx, dt.alice, "web", "bob", "boss"); err == nil {
		t.Error("unknown role: err = nil")
	}
	m, err := dt.s.SetMember(ctx, dt.alice, "web", " bob ", RoleMaintainer)
	if err != nil || m.UserID != dt.bob.UserID {
		t.Fatalf("SetMember = %+v, %v", m, err)
	}
	// A maintainer can't manage members.
	if _, err := dt.s.SetMember(ctx, dt.bob, "web", "bob", RoleOwner); !errors.Is(err, ErrForbidden) {
		t.Errorf("maintainer promoting himself: err = %v, want ErrForbidden", err)
	}
	// The only owner can't step down or leave.
	if _, err := dt.s.SetMember(ctx, dt.alice, "web", "alice", RoleDeployer); !errors.Is(err, ErrLastOwner) {
		t.Errorf("last owner demoted: err = %v, want ErrLastOwner", err)
	}
	if err := dt.s.RemoveMember(ctx, dt.alice, "web", dt.alice.UserID); !errors.Is(err, ErrLastOwner) {
		t.Errorf("last owner removed: err = %v, want ErrLastOwner", err)
	}
	if _, err := dt.s.SetMember(ctx, dt.alice, "web", "bob", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := dt.s.RemoveMember(ctx, dt.bob, "web", dt.alice.UserID); err != nil {
		t.Errorf("second owner removes the first: err = %v", err)
	}
	if err := dt.s.RemoveMember(ctx, dt.bob, "web", dt.alice.UserID); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove a non-member: err = %v, want ErrNotFound", err)
	}
	got, err := dt.s.Members(ctx, dt.bob, "web")
	if err != nil || len(got) != 1 || got[0].Username != "bob" || got[0].Role != RoleOwner {
		t.Errorf("Members = %+v, %v; want bob as owner", got, err)
	}
	// An admin manages members without being one.
	if _, err := dt.s.SetMember(ctx, dt.root, "web", "alice", RoleViewer); err != nil {
		t.Errorf("admin adds a member: err = %v", err)
	}
}

func TestTokens(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	dt.create(t, "web")
	dt.create(t, "api")
	tok, secret, err := dt.s.CreateToken(ctx, dt.alice, "web", TokenInput{Name: "ci", Scopes: []Scope{ScopeRunsRead, ScopeDeploy}})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(tok.Scopes) != "[deploy runs:read]" || tok.Prefix != secret[:tokenShownLen] || tok.CreatedBy != "alice" {
		t.Errorf("token = %+v", tok)
	}
	if _, _, err := dt.s.CreateToken(ctx, dt.alice, "web", TokenInput{Name: "ci", Scopes: []Scope{ScopeDeploy}}); !errors.Is(err, ErrTokenExists) {
		t.Errorf("same name: err = %v, want ErrTokenExists", err)
	}
	readOnly := time.UnixMilli(1_700_000_000_000).Add(time.Hour)
	_, reader, err := dt.s.CreateToken(ctx, dt.alice, "web", TokenInput{Name: "status", Scopes: []Scope{ScopeRunsRead}, ExpiresAt: &readOnly})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []TokenInput{
		{Name: "", Scopes: []Scope{ScopeDeploy}},
		{Name: " x", Scopes: []Scope{ScopeDeploy}},
		{Name: "x"},
		{Name: "x", Scopes: []Scope{"admin"}},
		{Name: "x", Scopes: []Scope{ScopeDeploy}, ExpiresAt: new(dt.clock.Now())},
	} {
		var inputErr *InputError
		if _, _, err := dt.s.CreateToken(ctx, dt.alice, "web", in); !errors.As(err, &inputErr) {
			t.Errorf("CreateToken(%+v): err = %v, want an InputError", in, err)
		}
	}
	if _, _, err := dt.s.CreateToken(ctx, dt.bob, "web", TokenInput{Name: "x", Scopes: []Scope{ScopeDeploy}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("non-member: err = %v, want ErrNotFound", err)
	}

	tests := []struct {
		name    string
		dep     string
		secret  string
		scope   Scope
		advance time.Duration
		wantErr error
	}{
		{name: "deploy", dep: "web", secret: secret, scope: ScopeDeploy},
		{name: "read", dep: "web", secret: reader, scope: ScopeRunsRead},
		{name: "missing scope", dep: "web", secret: reader, scope: ScopeDeploy, wantErr: ErrScope},
		{name: "other deployment", dep: "api", secret: secret, scope: ScopeDeploy, wantErr: ErrInvalidToken},
		{name: "unknown", dep: "web", secret: TokenPrefix + "NOPE", scope: ScopeDeploy, wantErr: ErrInvalidToken},
		{name: "API token shape", dep: "web", secret: "nudge_ABC", scope: ScopeDeploy, wantErr: ErrInvalidToken},
		{name: "invalid name", dep: "../web", secret: secret, scope: ScopeDeploy, wantErr: ErrInvalidToken},
		{name: "expired", dep: "web", secret: reader, scope: ScopeRunsRead, advance: time.Hour, wantErr: ErrInvalidToken},
	}
	for _, tt := range tests {
		dt.clock.Advance(tt.advance)
		d, got, err := dt.s.AuthenticateToken(ctx, tt.dep, tt.secret, tt.scope)
		if !errors.Is(err, tt.wantErr) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.wantErr)
			continue
		}
		if err == nil && (d.Name != tt.dep || d.Role != "" || got.Name == "") {
			t.Errorf("%s: deployment %+v, token %+v", tt.name, d, got)
		}
	}

	// The token reports its creator's role now, so the API can refuse a demoted one's.
	if _, got, err := dt.s.AuthenticateToken(ctx, "web", secret, ScopeDeploy); err != nil || got.CreatorID != dt.alice.UserID || got.CreatorRole != RoleOwner {
		t.Errorf("creator = %d %q, %v; want alice as owner", got.CreatorID, got.CreatorRole, err)
	}
	if _, err := dt.s.SetMember(ctx, dt.alice, "web", "bob", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := dt.s.RemoveMember(ctx, dt.bob, "web", dt.alice.UserID); err != nil {
		t.Fatal(err)
	}
	if _, got, err := dt.s.AuthenticateToken(ctx, "web", secret, ScopeDeploy); err != nil || got.CreatorRole != "" {
		t.Errorf("creator after removal = %q, %v; want no role", got.CreatorRole, err)
	}
	if _, err := dt.s.SetMember(ctx, dt.bob, "web", "alice", RoleOwner); err != nil {
		t.Fatal(err)
	}

	list, err := dt.s.Tokens(ctx, dt.alice, "web")
	if err != nil || len(list) != 2 || list[0].LastUsedAt == nil {
		t.Fatalf("Tokens = %+v, %v; want ci (used) and status", list, err)
	}
	if err := dt.s.DeleteToken(ctx, dt.alice, "web", list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dt.s.AuthenticateToken(ctx, "web", secret, ScopeDeploy); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("revoked: err = %v, want ErrInvalidToken", err)
	}
	if err := dt.s.DeleteToken(ctx, dt.alice, "web", list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke twice: err = %v, want ErrNotFound", err)
	}
}

// deployer is a DeployFunc the test steps through: each run reports on started and
// waits for its outcome on release.
type deployer struct {
	started chan Run
	release chan Outcome
	// reports gets each run's Report, so a test can send progress.
	reports chan Report
}

func newDeployer() *deployer {
	return &deployer{started: make(chan Run, 4), release: make(chan Outcome), reports: make(chan Report, 4)}
}

func (dp *deployer) deploy(ctx context.Context, _ Deployment, r Run, report Report) Outcome {
	dp.reports <- report
	dp.started <- r
	select {
	case out := <-dp.release:
		return out
	case <-ctx.Done():
		return Outcome{Status: StatusFailed, Error: "cancelled"}
	}
}

func TestDeployRuns(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	d := dt.create(t, "web", "API_IMAGE")
	dp := newDeployer()
	req := func(vars map[string]string) Request {
		return Request{ActorKind: ActorToken, ActorName: "ci", Variables: vars}
	}

	var inputErr *InputError
	if _, err := dt.s.Deploy(ctx, d, req(map[string]string{"OTHER": "x"}), dp.deploy); !errors.As(err, &inputErr) {
		t.Errorf("variable not allowed: err = %v, want an InputError", err)
	}
	for _, bad := range []string{"", "a b", "${HOME}", "x\nY=1", "'q'"} {
		if _, err := dt.s.Deploy(ctx, d, req(map[string]string{"API_IMAGE": bad}), dp.deploy); !errors.As(err, &inputErr) {
			t.Errorf("value %q: err = %v, want an InputError", bad, err)
		}
	}

	first, err := dt.s.Deploy(ctx, d, req(map[string]string{"API_IMAGE": "registry.example/api@sha256:1"}), dp.deploy)
	if err != nil {
		t.Fatal(err)
	}
	if r := <-dp.started; r.ID != first.RunID || r.Variables["API_IMAGE"] != "registry.example/api@sha256:1" || r.Status != StatusRunning {
		t.Errorf("started run = %+v", r)
	}
	second, err := dt.s.Deploy(ctx, d, req(map[string]string{"API_IMAGE": "registry.example/api:2"}), dp.deploy)
	if err != nil {
		t.Fatal(err)
	}
	third, err := dt.s.Deploy(ctx, d, req(nil), dp.deploy)
	if err != nil {
		t.Fatal(err)
	}
	// The second waited behind the first and is replaced by the third, which takes on
	// its variables.
	<-second.Done()
	if r, err := dt.s.Run(ctx, d.ID, second.RunID); err != nil || r.Status != StatusSuperseded || r.FinishedAt == nil {
		t.Errorf("replaced run = %+v, %v; want superseded", r, err)
	}
	if r, err := dt.s.Run(ctx, d.ID, third.RunID); err != nil || r.Variables["API_IMAGE"] != "registry.example/api:2" {
		t.Errorf("replacing run = %+v, %v; want the replaced run's API_IMAGE", r, err)
	}

	dp.release <- Outcome{Status: StatusSucceeded, Previous: map[string]string{"API_IMAGE": "old"}, ImageBefore: "api=sha256:a", ImageAfter: "api=sha256:b"}
	<-first.Done()
	r, err := dt.s.Run(ctx, d.ID, first.RunID)
	if err != nil || r.Status != StatusSucceeded || r.Previous["API_IMAGE"] != "old" || r.ImageAfter != "api=sha256:b" || r.ActorName != "ci" {
		t.Errorf("first run = %+v, %v", r, err)
	}
	if r := <-dp.started; r.ID != third.RunID || r.Variables["API_IMAGE"] != "registry.example/api:2" {
		t.Errorf("next started = %+v, want the third %d with the merged variables", r, third.RunID)
	}
	dt.clock.after <- time.Time{}
	if r, finished, err := dt.s.Wait(ctx, d, third, time.Minute); err != nil || finished || r.Status != StatusRunning {
		t.Errorf("Wait timed out = %+v, %v, %v; want still running", r, finished, err)
	}
	dp.release <- Outcome{Status: "weird"}
	if r, finished, err := dt.s.Wait(ctx, d, third, time.Minute); err != nil || !finished || r.Status != StatusFailed {
		t.Errorf("Wait = %+v, %v, %v; want failed (an unknown status)", r, finished, err)
	}

	runs, err := dt.s.Runs(ctx, d.ID)
	if err != nil || len(runs) != 3 || runs[0].ID != third.RunID {
		t.Errorf("Runs = %+v, %v; want three, newest first", runs, err)
	}
	dt.s.mu.Lock()
	slots := len(dt.s.slots)
	dt.s.mu.Unlock()
	if slots != 0 {
		t.Errorf("%d slots left after the runs, want 0", slots)
	}
}

func TestDeployAndClose(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	d := dt.create(t, "web")
	dp := newDeployer()
	running, err := dt.s.Deploy(ctx, d, Request{ActorKind: ActorUser, ActorName: "alice"}, dp.deploy)
	if err != nil {
		t.Fatal(err)
	}
	<-dp.started
	queued, err := dt.s.Deploy(ctx, d, Request{ActorKind: ActorUser, ActorName: "alice"}, dp.deploy)
	if err != nil {
		t.Fatal(err)
	}
	dt.s.Close()
	for _, tk := range []Ticket{running, queued} {
		r, err := dt.s.Run(ctx, d.ID, tk.RunID)
		if err != nil || r.Status != StatusFailed || r.Error == "" {
			t.Errorf("run %d after Close = %+v, %v; want failed", tk.RunID, r, err)
		}
	}
	if _, err := dt.s.Deploy(ctx, d, Request{ActorKind: ActorUser, ActorName: "alice"}, dp.deploy); !errors.Is(err, ErrClosed) {
		t.Errorf("Deploy after Close: err = %v, want ErrClosed", err)
	}
}

func TestDeployDeleted(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	d := dt.create(t, "web")
	if err := dt.s.Delete(ctx, dt.alice, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := dt.s.Deploy(ctx, d, Request{ActorKind: ActorUser, ActorName: "alice"}, newDeployer().deploy); !errors.Is(err, ErrNotFound) {
		t.Errorf("Deploy of a deleted deployment: err = %v, want ErrNotFound", err)
	}
}

func TestFailStale(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	d := dt.create(t, "web")
	id, err := dt.s.insertRun(ctx, Run{Deployment: d.ID, ActorKind: ActorUser, ActorName: "alice", Status: StatusQueued, Variables: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := dt.s.FailStale(ctx); err != nil {
		t.Fatal(err)
	}
	if r, err := dt.s.Run(ctx, d.ID, id); err != nil || r.Status != StatusFailed {
		t.Errorf("stale run = %+v, %v; want failed", r, err)
	}
}

func TestSetVariables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		env      string
		vars     map[string]string
		want     string
		previous map[string]string
	}{
		{
			name: "replace in place", env: "# images\nAPI_IMAGE=api:1\nOTHER=x\n",
			vars: map[string]string{"API_IMAGE": "api:2"},
			want: "# images\nAPI_IMAGE=api:2\nOTHER=x\n", previous: map[string]string{"API_IMAGE": "api:1"},
		},
		{
			name: "export and quotes", env: "export API_IMAGE=\"api:1\"\n",
			vars: map[string]string{"API_IMAGE": "api:2"},
			want: "API_IMAGE=api:2\n", previous: map[string]string{"API_IMAGE": "api:1"},
		},
		{
			name: "append sorted", env: "OTHER=x",
			vars: map[string]string{"B": "2", "A": "1"},
			want: "OTHER=x\nA=1\nB=2\n", previous: map[string]string{"A": "", "B": ""},
		},
		{
			name: "empty file", env: "",
			vars: map[string]string{"A": "1"},
			want: "A=1\n", previous: map[string]string{"A": ""},
		},
		{
			name: "comment not a variable", env: "# A=old\n",
			vars: map[string]string{"A": "1"},
			want: "# A=old\nA=1\n", previous: map[string]string{"A": ""},
		},
		{
			name: "repeated line", env: "A=1\nA=2\n",
			vars: map[string]string{"A": "3"},
			want: "A=3\nA=3\n", previous: map[string]string{"A": "1"},
		},
		{name: "nothing to set", env: "A=1\n", vars: map[string]string{}, want: "A=1\n", previous: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, prev := SetVariables(tt.env, tt.vars)
			if got != tt.want || !maps.Equal(prev, tt.previous) {
				t.Errorf("SetVariables = %q, %v; want %q, %v", got, prev, tt.want, tt.previous)
			}
		})
	}
}

func TestHealthSettings(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	d, err := dt.s.Create(ctx, dt.alice, Input{Name: "shop", Environment: dt.env, Stack: "shop",
		Health: Health{WaitSeconds: 60, HTTPChecks: []HTTPCheck{{Service: "web", Port: 8080}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := HTTPCheck{Service: "web", Port: 8080, Path: "/", Status: 200, TimeoutSeconds: 5}
	if d.Health.WaitSeconds != 60 || !slices.Equal(d.Health.HTTPChecks, []HTTPCheck{want}) || d.Health.Rollback {
		t.Errorf("created health = %+v", d.Health)
	}
	// Health stored before the list of checks holds one "http" check.
	if _, err := dt.s.db.ExecContext(ctx, `UPDATE deployments SET health = ? WHERE name = 'shop'`,
		`{"wait_seconds":60,"stable_seconds":0,"http":{"service":"web","port":8080,"path":"/","status":200,"timeout_seconds":5},"rollback":false}`); err != nil {
		t.Fatal(err)
	}
	if d, err = dt.s.Authorize(ctx, dt.alice, "shop", ActionView); err != nil || !slices.Equal(d.Health.HTTPChecks, []HTTPCheck{want}) || d.Health.WaitSeconds != 60 {
		t.Errorf("old stored health = %+v, %v", d.Health, err)
	}

	d, err = dt.s.Change(ctx, dt.alice, "shop", Update{Health: &Health{StableSeconds: 30, Rollback: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Health.HTTPChecks) != 0 || d.Health.StableSeconds != 30 || !d.Health.Rollback || d.Health.WaitSeconds != 0 {
		t.Errorf("changed health = %+v", d.Health)
	}
	desc := "kept"
	if d, err = dt.s.Change(ctx, dt.alice, "shop", Update{Description: &desc}); err != nil || !d.Health.Rollback {
		t.Errorf("health after a change without it = %+v, %v", d.Health, err)
	}

	check := func(c HTTPCheck) *Health { return &Health{HTTPChecks: []HTTPCheck{{Service: "ok", Port: 80}, c}} }
	for name, h := range map[string]*Health{
		"wait":         {WaitSeconds: MaxWaitSeconds + 1},
		"stable":       {StableSeconds: -1},
		"service":      check(HTTPCheck{Service: "web app", Port: 80}),
		"port":         check(HTTPCheck{Service: "web", Port: 70000}),
		"path":         check(HTTPCheck{Service: "web", Port: 80, Path: "health"}),
		"status":       check(HTTPCheck{Service: "web", Port: 80, Status: 700}),
		"timeout":      check(HTTPCheck{Service: "web", Port: 80, TimeoutSeconds: 61}),
		"path newline": check(HTTPCheck{Service: "web", Port: 80, Path: "/a\nb"}),
		"too many":     {HTTPChecks: make([]HTTPCheck, MaxHTTPChecks+1)},
	} {
		var inputErr *InputError
		if _, err := dt.s.Change(ctx, dt.alice, "shop", Update{Health: h}); !errors.As(err, &inputErr) {
			t.Errorf("%s: err = %v, want an InputError", name, err)
		}
	}
}

func TestFollowProgress(t *testing.T) {
	t.Parallel()
	dt := newDepTest(t)
	ctx := context.Background()
	d := dt.create(t, "web")
	dp := newDeployer()
	ticket, err := dt.s.Deploy(ctx, d, Request{ActorKind: ActorUser, ActorName: "alice"}, dp.deploy)
	if err != nil {
		t.Fatal(err)
	}
	report := <-dp.reports
	<-dp.started
	report(Progress{Step: StepPulling})

	progress, stop := dt.s.Follow(ticket)
	defer stop()
	if p := <-progress; p.Step != StepPulling {
		t.Errorf("first progress = %+v, want the current step", p)
	}
	report(Progress{Step: StepChecking, Service: "api", Message: "HTTP check skipped", Warning: true})
	if p := <-progress; p.Step != StepChecking || p.Service != "api" || !p.Warning {
		t.Errorf("progress = %+v", p)
	}
	report(Progress{Message: "waiting"})
	if p := <-progress; p.Step != StepChecking || p.Message != "waiting" {
		t.Errorf("progress without a step = %+v, want the current step", p)
	}
	if r, err := dt.s.Run(ctx, d.ID, ticket.RunID); err != nil || r.Step != StepChecking {
		t.Errorf("running run = %+v, %v; want step checking", r, err)
	}

	dp.release <- Outcome{Status: StatusSucceeded}
	for range progress {
	}
	<-ticket.Done()
	r, err := dt.s.Run(ctx, d.ID, ticket.RunID)
	if err != nil || r.Step != StepChecking || len(r.Warnings) != 1 || r.Warnings[0] != "HTTP check skipped" {
		t.Errorf("finished run = %+v, %v", r, err)
	}
	if after, stop := dt.s.Follow(ticket); true {
		defer stop()
		if _, open := <-after; open {
			t.Error("following a finished run got progress")
		}
	}
}
