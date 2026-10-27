package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// testParams keep hashing fast in tests.
var testParams = Params{Memory: 64, Time: 1, Threads: 1}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	return New(Options{DB: st.DB(), Now: clock.Now, Params: testParams}), clock
}

func TestPasswordHash(t *testing.T) {
	t.Parallel()
	hash, err := hashPassword("correct horse", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("hash = %q", hash)
	}
	other, err := hashPassword("correct horse", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Error("two hashes of one password are equal: salt not random")
	}
	tests := []struct {
		name    string
		hash    string
		pw      string
		want    bool
		wantErr bool
	}{
		{name: "match", hash: hash, pw: "correct horse", want: true},
		{name: "mismatch", hash: hash, pw: "correct horsf"},
		{name: "empty", hash: hash, pw: ""},
		{name: "other algorithm", hash: strings.Replace(hash, "argon2id", "argon2i", 1), pw: "correct horse", wantErr: true},
		{name: "huge memory", hash: strings.Replace(hash, "m=64", "m=99999999", 1), pw: "correct horse", wantErr: true},
		{name: "truncated", hash: hash[:20], pw: "correct horse", wantErr: true},
		{name: "garbage", hash: "plaintext", pw: "plaintext", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := verifyPassword(tt.hash, tt.pw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("verify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateFirstUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	if needed, err := s.SetupNeeded(ctx); err != nil || !needed {
		t.Fatalf("SetupNeeded = %v, %v; want true", needed, err)
	}

	var inputErr *InputError
	for _, tc := range []struct{ user, pw string }{
		{"", "longenough"},
		{" admin", "longenough"},
		{"ad\x00min", "longenough"},
		{strings.Repeat("a", 65), "longenough"},
		{"admin", "short"},
		{"admin", strings.Repeat("x", 1025)},
	} {
		if _, err := s.CreateFirstUser(ctx, tc.user, tc.pw); !errors.As(err, &inputErr) {
			t.Errorf("CreateFirstUser(%q, %d bytes) err = %v, want InputError", tc.user, len(tc.pw), err)
		}
	}

	// Concurrent setups create exactly one user.
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() { _, errs[i] = s.CreateFirstUser(ctx, "admin", "longenough") })
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, ErrSetupDone):
			t.Errorf("err = %v", err)
		}
	}
	if created != 1 {
		t.Errorf("created %d users, want 1", created)
	}
	if needed, err := s.SetupNeeded(ctx); err != nil || needed {
		t.Errorf("SetupNeeded = %v, %v; want false", needed, err)
	}
	if ok, err := s.EnsureAdmin(ctx, "longenough"); err != nil || ok {
		t.Errorf("EnsureAdmin = %v, %v; want false, nil", ok, err)
	}
}

func TestEnsureAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	if ok, err := s.EnsureAdmin(ctx, "short"); err == nil || ok {
		t.Errorf("EnsureAdmin(short) = %v, %v; want error", ok, err)
	}
	if ok, err := s.EnsureAdmin(ctx, "longenough"); err != nil || !ok {
		t.Fatalf("EnsureAdmin = %v, %v; want true", ok, err)
	}
	if _, err := s.Login(ctx, "10.0.0.1", "admin", "longenough"); err != nil {
		t.Errorf("Login as admin: %v", err)
	}
}

func TestLoginAndSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	u, err := s.CreateFirstUser(ctx, "Admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ user, pw string }{{"Admin", "wrong-password"}, {"nobody", "longenough"}} {
		if _, err := s.Login(ctx, "10.0.0.1", tc.user, tc.pw); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Login(%q) err = %v, want ErrInvalidCredentials", tc.user, err)
		}
	}

	// The username is case-insensitive.
	sess, err := s.Login(ctx, "10.0.0.1", "admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	if sess.User.ID != u.ID || sess.User.Username != "Admin" || sess.Token == "" {
		t.Errorf("session = %+v", sess)
	}
	if want := clock.Now().Add(DefaultSessionTTL); !sess.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", sess.ExpiresAt, want)
	}

	// Only the hash is stored.
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE id_hash = ?`, sess.Token).Scan(&n); err != nil || n != 0 {
		t.Errorf("token stored in clear: count %d, err %v", n, err)
	}

	got, err := s.SessionUser(ctx, sess.Token)
	if err != nil || got.ID != u.ID {
		t.Fatalf("SessionUser = %+v, %v", got, err)
	}
	for _, tok := range []string{"", "bogus"} {
		if _, err := s.SessionUser(ctx, tok); !errors.Is(err, ErrNoSession) {
			t.Errorf("SessionUser(%q) err = %v, want ErrNoSession", tok, err)
		}
	}

	// Logout ends the session.
	if _, err := s.Logout(ctx, sess.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, sess.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("after logout err = %v, want ErrNoSession", err)
	}

	// A session expires after the TTL and is deleted on lookup.
	sess, err = s.Login(ctx, "10.0.0.1", "admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(DefaultSessionTTL - time.Second)
	if _, err := s.SessionUser(ctx, sess.Token); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
	clock.Advance(time.Second)
	if _, err := s.SessionUser(ctx, sess.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("at expiry err = %v, want ErrNoSession", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions left = %d, %v; want 0", n, err)
	}
}

func TestSessionTouchesLastSeen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	if _, err := s.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Login(ctx, "10.0.0.1", "admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	lastSeen := func() int64 {
		var v int64
		if err := s.db.QueryRow(`SELECT last_seen_at FROM sessions`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	start := lastSeen()
	clock.Advance(30 * time.Second)
	if _, err := s.SessionUser(ctx, sess.Token); err != nil {
		t.Fatal(err)
	}
	if got := lastSeen(); got != start {
		t.Errorf("last_seen_at moved within touchInterval: %d -> %d", start, got)
	}
	clock.Advance(30 * time.Second)
	if _, err := s.SessionUser(ctx, sess.Token); err != nil {
		t.Fatal(err)
	}
	if got, want := lastSeen(), clock.Now().Unix(); got != want {
		t.Errorf("last_seen_at = %d, want %d", got, want)
	}
}

func TestCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	if _, err := s.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.Login(ctx, "10.0.0.1", "admin", "longenough"); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Hour)
	fresh, err := s.Login(ctx, "10.0.0.1", "admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "10.0.0.2", "admin", "nope-nope"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	clock.Advance(DefaultSessionTTL - time.Hour)
	n, err := s.Cleanup(ctx)
	if err != nil || n != 2 {
		t.Errorf("Cleanup = %d, %v; want 2", n, err)
	}
	if _, err := s.SessionUser(ctx, fresh.Token); err != nil {
		t.Errorf("fresh session gone: %v", err)
	}
	if l := s.ipLimit.len() + s.userLimit.len(); l != 0 {
		t.Errorf("limiter keys left = %d, want 0", l)
	}
}

func TestRunCleanupStopsOnCancel(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RunCleanup(ctx, time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunCleanup did not stop")
	}
}

func TestLoginRateLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	if _, err := s.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}

	// Per username: from many IPs, the username locks after userFailureLimit failures,
	// even for the right password.
	for i := range userFailureLimit {
		if _, err := s.Login(ctx, ip(i), "admin", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var rl *RateLimitedError
	if _, err := s.Login(ctx, "10.9.9.9", "ADMIN", "longenough"); !errors.As(err, &rl) {
		t.Fatalf("err = %v, want RateLimitedError", err)
	}
	if rl.RetryAfter <= 0 || rl.RetryAfter > failureWindow {
		t.Errorf("RetryAfter = %v", rl.RetryAfter)
	}
	clock.Advance(failureWindow)
	if _, err := s.Login(ctx, "10.9.9.9", "admin", "longenough"); err != nil {
		t.Fatalf("after the window: %v", err)
	}

	// Per IP: one IP trying many usernames.
	for i := range ipFailureLimit {
		_, err := s.Login(ctx, "10.1.1.1", fmt.Sprintf("user%d", i), "wrong-password")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Login(ctx, "10.1.1.1", "admin", "longenough"); !errors.As(err, &rl) {
		t.Fatalf("err = %v, want RateLimitedError", err)
	}
	if _, err := s.Login(ctx, "10.1.1.2", "admin", "longenough"); err != nil {
		t.Errorf("other IP: %v", err)
	}

	// Oversized usernames count against the IP but never become limiter keys.
	before := s.userLimit.len()
	if _, err := s.Login(ctx, "10.2.2.2", strings.Repeat("u", 10000), "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("long username err = %v", err)
	}
	if s.userLimit.len() != before {
		t.Error("long username stored in the limiter")
	}
}

func ip(i int) string { return fmt.Sprintf("10.0.0.%d", i) }

func TestLimiterBounded(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	l := newLimiter(1, time.Minute, 3)
	for i, k := range []string{"a", "b", "c", "d"} {
		if _, ok := l.reserve(k, now.Add(time.Duration(i)*time.Second)); !ok {
			t.Fatalf("first reserve of %s refused", k)
		}
	}
	if l.len() != 3 {
		t.Errorf("len = %d, want 3", l.len())
	}
	if _, ok := l.reserve("d", now.Add(5*time.Second)); ok {
		t.Error("newest key d not blocked")
	}
	if _, ok := l.reserve("a", now.Add(5*time.Second)); !ok {
		t.Error("oldest key a not evicted")
	}
	// Expired windows go first.
	l.reserve("e", now.Add(2*time.Minute))
	if l.len() != 1 {
		t.Errorf("len after expiry = %d, want 1", l.len())
	}
}

func TestLimiterRelease(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	l := newLimiter(2, time.Minute, 10)
	l.reserve("k", now)
	l.reserve("k", now)
	if _, ok := l.reserve("k", now); ok {
		t.Fatal("third attempt allowed")
	}
	l.release("k", now)
	if _, ok := l.reserve("k", now); !ok {
		t.Error("released attempt not handed back")
	}
	// A release from an earlier window doesn't touch the new one.
	later := now.Add(2 * time.Minute)
	l.reserve("k", later)
	l.release("k", now)
	l.reserve("k", later)
	if _, ok := l.reserve("k", later); ok {
		t.Error("stale release freed an attempt in the new window")
	}
	l.release("missing", now) // no panic, no entry
	if l.len() != 1 {
		t.Errorf("len = %d, want 1", l.len())
	}
}

func TestLoginParallelRespectsLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	if _, err := s.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	const n = 40
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { _, errs[i] = s.Login(ctx, ip(i), "admin", "wrong-password") })
	}
	wg.Wait()
	guesses := 0
	for _, err := range errs {
		var rl *RateLimitedError
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			guesses++
		case errors.As(err, &rl), errors.Is(err, ErrBusy):
		default:
			t.Errorf("err = %v", err)
		}
	}
	if guesses > userFailureLimit {
		t.Errorf("%d password guesses, limit %d", guesses, userFailureLimit)
	}
}

func TestLoginBusy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	if _, err := s.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	for range maxConcurrentHashes {
		s.hashes <- struct{}{}
	}
	for range maxHashWaiters {
		s.waiters <- struct{}{}
	}
	if _, err := s.Login(ctx, "10.0.0.1", "admin", "longenough"); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if n := s.ipLimit.len() + s.userLimit.len(); n != 0 {
		t.Errorf("a busy refusal was counted: %d limiter keys", n)
	}
}

func TestLoginCounting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	if _, err := s.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	// Successful logins don't use up the IP's attempts.
	for i := range ipFailureLimit + 5 {
		if _, err := s.Login(ctx, "10.0.0.1", "admin", "longenough"); err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
	}
	// Unknown usernames count for the IP only.
	if _, err := s.Login(ctx, "10.0.0.2", "ghost", "longenough"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	if s.userLimit.len() != 0 || s.ipLimit.len() != 1 {
		t.Errorf("limiter keys: user %d, ip %d; want 0, 1", s.userLimit.len(), s.ipLimit.len())
	}
}

func TestAttemptSharesTheLoginLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	if _, err := s.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	// Valid credentials hand their attempt back.
	for range ipFailureLimit * 2 {
		ok, err := s.Attempt("10.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		ok()
	}
	// Failed webhook checks and failed logins fill the same window.
	for i := range ipFailureLimit - 1 {
		if _, err := s.Attempt("10.0.0.1"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Login(ctx, "10.0.0.1", "admin", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login = %v", err)
	}
	var rl *RateLimitedError
	if _, err := s.Attempt("10.0.0.1"); !errors.As(err, &rl) {
		t.Fatalf("attempt over the limit = %v, want RateLimitedError", err)
	}
	if _, err := s.Login(ctx, "10.0.0.1", "admin", "longenough"); !errors.As(err, &rl) {
		t.Fatalf("login over the limit = %v, want RateLimitedError", err)
	}
	if _, err := s.Attempt("10.0.0.2"); err != nil {
		t.Errorf("another IP = %v", err)
	}
	clock.Advance(failureWindow)
	if _, err := s.Attempt("10.0.0.1"); err != nil {
		t.Errorf("after the window = %v", err)
	}
}
