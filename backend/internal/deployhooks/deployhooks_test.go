package deployhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
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

type hookTest struct {
	svc   *Service
	clock *fakeClock
	// alice and bob are users; env an environment.
	alice, bob Actor
	env        int64
}

func newHookTest(t *testing.T) hookTest {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ht := hookTest{clock: &fakeClock{now: time.Unix(1_700_000_000, 0), after: make(chan time.Time, 1)}}
	for _, name := range []string{"alice", "bob"} {
		res, err := st.DB().ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, 'x', 0, 0)`, name)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if name == "alice" {
			ht.alice = Actor{UserID: id}
		} else {
			ht.bob = Actor{UserID: id}
		}
	}
	res, err := st.DB().ExecContext(ctx, `INSERT INTO environments (name, kind, created_at) VALUES ('local', 'local', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	ht.env, _ = res.LastInsertId()
	ht.svc = New(Options{DB: st.DB(), Box: box, Clock: ht.clock})
	t.Cleanup(ht.svc.Close)
	return ht
}

func (ht hookTest) create(t *testing.T, actor Actor, name string) (Hook, string) {
	t.Helper()
	h, secret, err := ht.svc.Create(context.Background(), actor, Input{Name: name, Environment: ht.env, Kind: KindContainer, Target: "web"})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return h, secret
}

func sign(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func TestCreateValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   Input
		want string
	}{
		{name: "no name", in: Input{Kind: KindContainer, Target: "web"}, want: "name must be"},
		{name: "long name", in: Input{Name: strings.Repeat("n", 65), Kind: KindContainer, Target: "web"}, want: "name must be"},
		{name: "control character", in: Input{Name: "a\nb", Kind: KindContainer, Target: "web"}, want: "name must be"},
		{name: "bad kind", in: Input{Name: "ci", Kind: "volume", Target: "web"}, want: "container or a stack"},
		{name: "bad target", in: Input{Name: "ci", Kind: KindStack, Target: "../x"}, want: "stack name is invalid"},
		{name: "bad pattern", in: Input{Name: "ci", Kind: KindContainer, Target: "web", TagFilter: "v1.["}, want: "not a valid pattern"},
		{name: "slash in filter", in: Input{Name: "ci", Kind: KindContainer, Target: "web", TagFilter: "a/b"}, want: "tag pattern"},
	}
	ht := newHookTest(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.in.Environment = ht.env
			_, _, err := ht.svc.Create(context.Background(), ht.alice, tt.in)
			var inputErr *InputError
			if !errors.As(err, &inputErr) || !strings.Contains(inputErr.Msg, tt.want) {
				t.Errorf("Create = %v, want an input error saying %q", err, tt.want)
			}
		})
	}
}

func TestCreateListDeleteRotate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, secret := ht.create(t, ht.alice, "ci")
	if !ValidID(h.ID) || !strings.HasPrefix(secret, SecretPrefix) || len(secret) != len(SecretPrefix)+43 {
		t.Errorf("id %q, secret %q: want a random ID and nudge_hook_ with 32 bytes", h.ID, secret)
	}
	if h.Owner != "alice" || h.Target != "web" || h.Kind != KindContainer || !h.CreatedAt.Equal(ht.clock.Now()) {
		t.Errorf("hook = %+v", h)
	}
	if _, _, err := ht.svc.Create(ctx, ht.alice, Input{Name: "ci", Environment: ht.env, Kind: KindStack, Target: "app"}); !errors.Is(err, ErrTaken) {
		t.Errorf("same name = %v, want ErrTaken", err)
	}
	// Names are per user.
	ht.create(t, ht.bob, "ci")
	stack, _, err := ht.svc.Create(ctx, ht.alice, Input{Name: "app", Environment: ht.env, Kind: KindStack, Target: "app"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := ht.svc.List(ctx, ht.alice, ht.env, "", "")
	if err != nil || len(list) != 2 || list[0].Name != "app" || list[1].ID != h.ID {
		t.Errorf("alice's list = %+v, %v; want app and ci", list, err)
	}
	list, _ = ht.svc.List(ctx, Actor{UserID: ht.bob.UserID, Admin: true}, ht.env, "", "")
	if len(list) != 3 {
		t.Errorf("admin's list = %+v, want all 3", list)
	}
	list, _ = ht.svc.List(ctx, ht.alice, ht.env, KindStack, "app")
	if len(list) != 1 || list[0].ID != stack.ID {
		t.Errorf("stack filter = %+v", list)
	}
	list, _ = ht.svc.List(ctx, ht.alice, ht.env+1, "", "")
	if len(list) != 0 {
		t.Errorf("other environment = %+v, want none", list)
	}

	_, newSecret, err := ht.svc.Rotate(ctx, ht.alice, ht.env, h.ID)
	if err != nil || newSecret == secret || !strings.HasPrefix(newSecret, SecretPrefix) {
		t.Fatalf("Rotate = %q, %v", newSecret, err)
	}
	bearer := func(s string) http.Header { return http.Header{"Authorization": {"Bearer " + s}} }
	if _, _, err := ht.svc.Authenticate(ctx, h.ID, bearer(secret), nil); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("old secret = %v, want ErrUnauthorized", err)
	}
	if _, _, err := ht.svc.Authenticate(ctx, h.ID, bearer(newSecret), nil); err != nil {
		t.Errorf("new secret = %v", err)
	}

	if err := ht.svc.Delete(ctx, ht.alice, ht.env, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ht.svc.Get(ctx, ht.alice, ht.env, h.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v", err)
	}
	if _, _, err := ht.svc.Authenticate(ctx, h.ID, bearer(newSecret), nil); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("deleted webhook = %v, want ErrUnauthorized", err)
	}
}

func TestCreateLimit(t *testing.T) {
	t.Parallel()
	ht := newHookTest(t)
	for i := range MaxPerUser {
		ht.create(t, ht.alice, "hook"+strconv.Itoa(i))
	}
	if _, _, err := ht.svc.Create(context.Background(), ht.alice, Input{Name: "one more", Environment: ht.env, Kind: KindContainer, Target: "web"}); !errors.Is(err, ErrTooMany) {
		t.Errorf("Create over the limit = %v, want ErrTooMany", err)
	}
	ht.create(t, ht.bob, "one more")
}

func TestOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	admin := Actor{UserID: ht.bob.UserID, Admin: true}
	tests := []struct {
		name  string
		actor Actor
		env   int64
		want  error
	}{
		{name: "owner", actor: ht.alice, env: ht.env},
		{name: "admin", actor: admin, env: ht.env},
		{name: "another user", actor: ht.bob, env: ht.env, want: ErrNotFound},
		{name: "other environment", actor: ht.alice, env: ht.env + 1, want: ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ht.svc.Get(ctx, tt.actor, tt.env, h.ID); !errors.Is(err, tt.want) {
				t.Errorf("Get = %v, want %v", err, tt.want)
			}
			if _, err := ht.svc.Runs(ctx, tt.actor, tt.env, h.ID); !errors.Is(err, tt.want) {
				t.Errorf("Runs = %v, want %v", err, tt.want)
			}
			if tt.want != nil {
				if _, _, err := ht.svc.Rotate(ctx, tt.actor, tt.env, h.ID); !errors.Is(err, tt.want) {
					t.Errorf("Rotate = %v, want %v", err, tt.want)
				}
				if err := ht.svc.Delete(ctx, tt.actor, tt.env, h.ID); !errors.Is(err, tt.want) {
					t.Errorf("Delete = %v, want %v", err, tt.want)
				}
			}
		})
	}
	if err := ht.svc.Delete(ctx, admin, ht.env, h.ID); err != nil {
		t.Errorf("admin Delete = %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()
	ht := newHookTest(t)
	h, secret := ht.create(t, ht.alice, "ci")
	_, other := ht.create(t, ht.alice, "other")
	body := `{"action":"created"}`
	tests := []struct {
		name   string
		id     string
		header map[string]string
		body   string
		want   Trigger
	}{
		{name: "bearer", header: map[string]string{"Authorization": "Bearer " + secret}, want: TriggerBearer},
		{name: "bearer scheme in lower case", header: map[string]string{"Authorization": "bearer " + secret}, want: TriggerBearer},
		{name: "wrong bearer", header: map[string]string{"Authorization": "Bearer " + secret + "x"}},
		{name: "another webhook's bearer", header: map[string]string{"Authorization": "Bearer " + other}},
		{name: "basic auth", header: map[string]string{"Authorization": "Basic " + secret}},
		// A webhook bearer is the credential even next to a valid signature.
		{name: "wrong bearer with a signature", header: map[string]string{"Authorization": "Bearer " + secret + "x", "X-Forgejo-Signature": sign(secret, body)}},
		// Any other Authorization header (a proxy's, an API token) leaves the signature.
		{name: "basic auth with a signature", header: map[string]string{"Authorization": "Basic dXNlcjpwYXNz", "X-Forgejo-Signature": sign(secret, body)}, want: TriggerSignature},
		{name: "API token with a signature", header: map[string]string{"Authorization": "Bearer nudge_ABC", "X-Hub-Signature-256": "sha256=" + sign(secret, body)}, want: TriggerSignature},
		{name: "forgejo signature", header: map[string]string{"X-Forgejo-Signature": sign(secret, body)}, want: TriggerSignature},
		{name: "gitea signature", header: map[string]string{"X-Gitea-Signature": sign(secret, body)}, want: TriggerSignature},
		{name: "github signature", header: map[string]string{"X-Hub-Signature-256": "sha256=" + sign(secret, body)}, want: TriggerSignature},
		{name: "github signature without prefix", header: map[string]string{"X-Hub-Signature-256": sign(secret, body)}},
		{name: "signature in upper case hex", header: map[string]string{"X-Forgejo-Signature": strings.ToUpper(sign(secret, body))}, want: TriggerSignature},
		{name: "signature over another body", header: map[string]string{"X-Forgejo-Signature": sign(secret, body)}, body: `{"action":"deleted"}`},
		{name: "signature of another webhook", header: map[string]string{"X-Forgejo-Signature": sign(other, body)}},
		{name: "signature not hex", header: map[string]string{"X-Gitea-Signature": "zz"}},
		{name: "short signature", header: map[string]string{"X-Gitea-Signature": sign(secret, body)[:32]}},
		{name: "no credential"},
		{name: "unknown webhook", id: "AAAAAAAAAAAAAAAAAAAAAAAAAA", header: map[string]string{"Authorization": "Bearer " + secret}},
		{name: "malformed id", id: "../x", header: map[string]string{"X-Forgejo-Signature": sign(secret, body)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			header := http.Header{}
			for k, v := range tt.header {
				header.Set(k, v)
			}
			id := h.ID
			if tt.id != "" {
				id = tt.id
			}
			b := body
			if tt.body != "" {
				b = tt.body
			}
			got, trigger, err := ht.svc.Authenticate(context.Background(), id, header, []byte(b))
			if tt.want == "" {
				if !errors.Is(err, ErrUnauthorized) {
					t.Errorf("Authenticate = %v, want ErrUnauthorized", err)
				}
				return
			}
			if err != nil || trigger != tt.want || got.ID != h.ID || got.Owner != "alice" {
				t.Errorf("Authenticate = %+v, %q, %v; want ci by %q", got, trigger, err, tt.want)
			}
		})
	}
}

// deployer is a DeployFunc the test steps: each deploy reports on started and waits for
// a value on release (or the end of its context).
type deployer struct {
	started chan string
	release chan Outcome
	mu      sync.Mutex
	calls   int
}

func newDeployer() *deployer {
	return &deployer{started: make(chan string, 10), release: make(chan Outcome)}
}

func (d *deployer) deploy(ctx context.Context, h Hook) Outcome {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	d.started <- h.ID
	select {
	case out := <-d.release:
		return out
	case <-ctx.Done():
		return Outcome{Status: StatusFailed, Error: "cancelled"}
	}
}

func (d *deployer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func waitDone(t *testing.T, tk Ticket) {
	t.Helper()
	select {
	case <-tk.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("run %d still going", tk.RunID)
	}
}

func waitStarted(t *testing.T, d *deployer) {
	t.Helper()
	select {
	case <-d.started:
	case <-time.After(5 * time.Second):
		t.Fatal("no deploy started")
	}
}

func (ht hookTest) run(t *testing.T, h Hook, id int64) Run {
	t.Helper()
	r, err := ht.svc.Run(context.Background(), h.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTriggerDebounce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	d := newDeployer()

	first, err := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, d)
	if r := ht.run(t, h, first.RunID); r.Status != StatusRunning || r.StartedAt == nil {
		t.Errorf("first run = %+v, want running", r)
	}
	// A trigger during the run queues a follow-up; more merge into it.
	second, err := ht.svc.Trigger(ctx, h, TriggerSignature, d.deploy)
	if err != nil {
		t.Fatal(err)
	}
	third, err := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
	if err != nil {
		t.Fatal(err)
	}
	if second.RunID == first.RunID || third.RunID != second.RunID {
		t.Errorf("runs %d, %d, %d: want the last two merged into a follow-up", first.RunID, second.RunID, third.RunID)
	}
	if r := ht.run(t, h, second.RunID); r.Status != StatusQueued || r.Trigger != TriggerSignature {
		t.Errorf("follow-up = %+v, want queued", r)
	}

	d.release <- Outcome{Status: StatusUpdated, ImageBefore: "sha256:a", ImageAfter: "sha256:b"}
	waitDone(t, first)
	waitStarted(t, d)
	d.release <- Outcome{Status: StatusUnchanged}
	waitDone(t, third)
	if d.count() != 2 {
		t.Errorf("deploys = %d, want 2", d.count())
	}

	runs, err := ht.svc.Runs(ctx, ht.alice, ht.env, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != second.RunID || runs[0].Status != StatusUnchanged ||
		runs[1].Status != StatusUpdated || runs[1].ImageBefore != "sha256:a" || runs[1].ImageAfter != "sha256:b" || runs[1].FinishedAt == nil {
		t.Errorf("runs = %+v", runs)
	}
	got, _ := ht.svc.Get(ctx, ht.alice, ht.env, h.ID)
	if got.LastTriggeredAt == nil || !got.LastTriggeredAt.Equal(ht.clock.Now()) {
		t.Errorf("last triggered = %v", got.LastTriggeredAt)
	}

	// Idle again: the next trigger runs at once.
	next, err := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, d)
	d.release <- Outcome{Status: StatusFailed, Error: "pull failed"}
	waitDone(t, next)
	if r := ht.run(t, h, next.RunID); r.Status != StatusFailed || r.Error != "pull failed" {
		t.Errorf("run = %+v", r)
	}
}

func TestTriggerIndependentHooks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	a, _ := ht.create(t, ht.alice, "a")
	b, _ := ht.create(t, ht.alice, "b")
	d := newDeployer()
	ta, _ := ht.svc.Trigger(ctx, a, TriggerBearer, d.deploy)
	tb, _ := ht.svc.Trigger(ctx, b, TriggerBearer, d.deploy)
	// Both run at once: one webhook's run doesn't queue another's.
	waitStarted(t, d)
	waitStarted(t, d)
	d.release <- Outcome{Status: StatusUnchanged}
	d.release <- Outcome{Status: StatusUnchanged}
	waitDone(t, ta)
	waitDone(t, tb)
}

func TestConcurrentTriggersMerge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	d := newDeployer()
	first, err := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, d)
	const n = TriggerLimit - 1
	tickets := make(chan Ticket, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			tk, err := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
			if err != nil {
				t.Error(err)
			}
			tickets <- tk
		})
	}
	wg.Wait()
	close(tickets)
	var follow Ticket
	for tk := range tickets {
		if follow.RunID == 0 {
			follow = tk
		}
		if tk.RunID != follow.RunID || tk.RunID == first.RunID {
			t.Errorf("run %d, want every trigger merged into one follow-up (%d)", tk.RunID, follow.RunID)
		}
	}
	runs, err := ht.svc.Runs(ctx, ht.alice, ht.env, h.ID)
	if err != nil || len(runs) != 2 {
		t.Errorf("runs = %+v, %v; want the first and one follow-up", runs, err)
	}
	d.release <- Outcome{Status: StatusUnchanged}
	waitStarted(t, d)
	d.release <- Outcome{Status: StatusUnchanged}
	waitDone(t, follow)
}

func TestTriggerRateLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	other, _ := ht.create(t, ht.alice, "other")
	done := func(context.Context, Hook) Outcome { return Outcome{Status: StatusUnchanged} }
	for i := range TriggerLimit {
		// Skips count too.
		if i%2 == 0 {
			if _, err := ht.svc.Skip(ctx, h, TriggerSignature); err != nil {
				t.Fatalf("skip %d: %v", i, err)
			}
			continue
		}
		if _, err := ht.svc.Trigger(ctx, h, TriggerBearer, done); err != nil {
			t.Fatalf("trigger %d: %v", i, err)
		}
	}
	ht.clock.Advance(20 * time.Second)
	var limited *RateLimitedError
	if _, err := ht.svc.Trigger(ctx, h, TriggerBearer, done); !errors.As(err, &limited) || limited.RetryAfter != 40*time.Second {
		t.Errorf("trigger over the limit = %v, want a 40s RateLimitedError", err)
	}
	if _, err := ht.svc.Skip(ctx, h, TriggerSignature); !errors.Is(err, ErrRateLimited) {
		t.Errorf("skip over the limit = %v", err)
	}
	if _, err := ht.svc.Trigger(ctx, other, TriggerBearer, done); err != nil {
		t.Errorf("another webhook = %v, want it allowed", err)
	}
	ht.clock.Advance(40 * time.Second)
	if _, err := ht.svc.Trigger(ctx, h, TriggerBearer, done); err != nil {
		t.Errorf("next window = %v", err)
	}
}

func TestWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	d := newDeployer()
	tk, err := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, d)

	// The wait times out while the run goes on.
	ht.clock.after <- time.Time{}
	run, finished, err := ht.svc.Wait(ctx, h, tk, time.Minute)
	if err != nil || finished || run.Status != StatusRunning {
		t.Errorf("Wait = %+v, %v, %v; want the running run, unfinished", run, finished, err)
	}
	// A caller that goes away stops waiting.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := ht.svc.Wait(cctx, h, tk, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait cancelled = %v", err)
	}

	d.release <- Outcome{Status: StatusUpdated}
	run, finished, err = ht.svc.Wait(ctx, h, tk, time.Minute)
	if err != nil || !finished || run.Status != StatusUpdated {
		t.Errorf("Wait = %+v, %v, %v; want the updated run", run, finished, err)
	}
}

func TestCloseStopsRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	d := newDeployer()
	first, _ := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
	waitStarted(t, d)
	queued, _ := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)

	ht.svc.Close()
	select {
	case <-first.Done():
	default:
		t.Fatal("Close returned before the run ended")
	}
	waitDone(t, queued)
	if d.count() != 1 {
		t.Errorf("deploys = %d, want only the first", d.count())
	}
	if r := ht.run(t, h, first.RunID); r.Status != StatusFailed || r.Error != "nudge shut down during the run" {
		t.Errorf("running run = %+v", r)
	}
	if r := ht.run(t, h, queued.RunID); r.Status != StatusFailed || r.StartedAt != nil || !strings.Contains(r.Error, "before the run started") {
		t.Errorf("queued run = %+v", r)
	}
	if _, err := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy); !errors.Is(err, ErrClosed) {
		t.Errorf("Trigger after Close = %v", err)
	}
}

func TestDeleteDropsQueuedRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	d := newDeployer()
	first, _ := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
	waitStarted(t, d)
	queued, _ := ht.svc.Trigger(ctx, h, TriggerBearer, d.deploy)
	if err := ht.svc.Delete(ctx, ht.alice, ht.env, h.ID); err != nil {
		t.Fatal(err)
	}
	d.release <- Outcome{Status: StatusUpdated}
	waitDone(t, first)
	waitDone(t, queued)
	if d.count() != 1 {
		t.Errorf("deploys = %d, want the queued one dropped", d.count())
	}
}

func TestRunsBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	for range RunsPerHook + 5 {
		if _, err := ht.svc.insertRun(ctx, h.ID, TriggerSignature, StatusSkipped, ht.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := ht.svc.Runs(ctx, ht.alice, ht.env, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != RunsPerHook || runs[0].ID <= runs[len(runs)-1].ID {
		t.Errorf("%d runs, want the newest %d first", len(runs), RunsPerHook)
	}
}

func TestFailStale(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ht := newHookTest(t)
	h, _ := ht.create(t, ht.alice, "ci")
	queued, _ := ht.svc.insertRun(ctx, h.ID, TriggerBearer, StatusQueued, ht.clock.Now())
	skipped, _ := ht.svc.insertRun(ctx, h.ID, TriggerBearer, StatusSkipped, ht.clock.Now())
	if err := ht.svc.FailStale(ctx); err != nil {
		t.Fatal(err)
	}
	if r := ht.run(t, h, queued); r.Status != StatusFailed || r.Error == "" {
		t.Errorf("stale run = %+v", r)
	}
	if r := ht.run(t, h, skipped); r.Status != StatusSkipped {
		t.Errorf("finished run = %+v", r)
	}
}

func TestSkipped(t *testing.T) {
	t.Parallel()
	pkg := func(version string) string {
		return `{"action":"created","package":{"name":"app","version":"` + version + `"}}`
	}
	tests := []struct {
		name    string
		filter  string
		trigger Trigger
		header  map[string]string
		body    string
		want    bool
	}{
		{name: "no filter", trigger: TriggerSignature, header: map[string]string{"X-Forgejo-Event": "package"}, body: pkg("v2")},
		{name: "matching version", filter: "v1.*", trigger: TriggerSignature, header: map[string]string{"X-Forgejo-Event": "package"}, body: pkg("v1.2")},
		{name: "other version", filter: "v1.*", trigger: TriggerSignature, header: map[string]string{"X-Forgejo-Event": "package"}, body: pkg("v2.0"), want: true},
		{name: "exact tag", filter: "latest", trigger: TriggerSignature, header: map[string]string{"X-Gitea-Event": "package"}, body: pkg("latest")},
		{name: "gitea event, other version", filter: "latest", trigger: TriggerSignature, header: map[string]string{"X-Gitea-Event": "package"}, body: pkg("v1"), want: true},
		{name: "push event", filter: "latest", trigger: TriggerSignature, header: map[string]string{"X-Forgejo-Event": "push"}, body: `{"ref":"main"}`},
		{name: "no event header", filter: "latest", trigger: TriggerSignature, body: pkg("v1")},
		{name: "bearer call", filter: "latest", trigger: TriggerBearer, header: map[string]string{"X-Forgejo-Event": "package"}, body: pkg("v1")},
		{name: "package event without JSON", filter: "latest", trigger: TriggerSignature, header: map[string]string{"X-Forgejo-Event": "package"}, body: "nope", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			header := http.Header{}
			for k, v := range tt.header {
				header.Set(k, v)
			}
			if got := Skipped(Hook{TagFilter: tt.filter}, tt.trigger, header, []byte(tt.body)); got != tt.want {
				t.Errorf("Skipped = %v, want %v", got, tt.want)
			}
		})
	}
}
