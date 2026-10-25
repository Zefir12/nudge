package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
)

var start = time.Unix(1_700_000_000, 0).UTC()

// fakeClock stands still until advanced; After records the wait and fires at once,
// except for the durations in hold, which never fire.
type fakeClock struct {
	mu     sync.Mutex
	waits  []time.Duration
	offset time.Duration
	hold   map[time.Duration]bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return start.Add(c.offset)
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hold[d] {
		return nil
	}
	c.waits = append(c.waits, d)
	ch := make(chan time.Time, 1)
	ch <- start
	return ch
}

func (c *fakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

func newTestService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{hold: map[time.Duration]bool{pruneEvery: true}}
	return New(Options{DB: st.DB(), Box: box, Version: "v1.2.3", Clock: clock}), clock
}

func ptr[T any](v T) *T { return &v }

// hook is a webhook endpoint that records what it receives.
type hook struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []*http.Request
	bodies []Message
	status int
}

func newHook(t *testing.T, status int) *hook {
	t.Helper()
	h := &hook{status: status}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m Message
		_ = json.NewDecoder(r.Body).Decode(&m)
		h.mu.Lock()
		h.reqs = append(h.reqs, r)
		h.bodies = append(h.bodies, m)
		status := h.status
		h.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hook) received() ([]*http.Request, []Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*http.Request(nil), h.reqs...), append([]Message(nil), h.bodies...)
}

func TestCreateValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   Input
		want string
	}{
		{"no name", Input{URL: ptr("https://x.example")}, "name must be"},
		{"spaces in name", Input{Name: " a", URL: ptr("https://x.example")}, "surrounding spaces"},
		{"no url", Input{Name: "a"}, "url is required"},
		{"ftp", Input{Name: "a", URL: ptr("ftp://x.example/")}, "http or https"},
		{"credentials", Input{Name: "a", URL: ptr("https://u:p@x.example/")}, "without credentials"},
		{"relative", Input{Name: "a", URL: ptr("/hook")}, "http or https"},
		{"control character", Input{Name: "a", URL: ptr("https://x.example/\n")}, "control characters"},
		{"bad header name", Input{Name: "a", URL: ptr("https://x.example"), HeaderName: "X Token", HeaderValue: ptr("v")}, "HTTP header name"},
		{"reserved header", Input{Name: "a", URL: ptr("https://x.example"), HeaderName: "content-type", HeaderValue: ptr("v")}, "set by nudge"},
		{"header without value", Input{Name: "a", URL: ptr("https://x.example"), HeaderName: "X-Token"}, "header value is required"},
		{"empty header value", Input{Name: "a", URL: ptr("https://x.example"), HeaderName: "X-Token", HeaderValue: ptr("")}, "header value must be"},
		{"header value newline", Input{Name: "a", URL: ptr("https://x.example"), HeaderName: "X-Token", HeaderValue: ptr("a\r\nB: c")}, "header value must be"},
		{"no events", Input{Name: "a", URL: ptr("https://x.example"), Events: []string{}}, "at least one event"},
		{"unknown event", Input{Name: "a", URL: ptr("https://x.example"), Events: []string{EventExited, "test"}}, `unknown event "test"`},
	}
	s, _ := newTestService(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := s.Create(context.Background(), tt.in)
			var inputErr *InputError
			if !errors.As(err, &inputErr) || !strings.Contains(inputErr.Msg, tt.want) {
				t.Errorf("err = %v, want an InputError with %q", err, tt.want)
			}
		})
	}
}

func TestWebhookCRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	w, err := s.Create(ctx, Input{Name: "chat", URL: ptr("https://hooks.example.com/T0/secret-token"), HeaderName: "x-token", HeaderValue: ptr("s3cret"), Enabled: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Endpoint != "https://hooks.example.com" || w.HeaderName != "X-Token" || !w.Enabled || w.LastSentAt != nil || !w.CreatedAt.Equal(start) {
		t.Errorf("created = %+v", w)
	}
	var urlEnc, headerEnc []byte
	if err := s.db.QueryRowContext(ctx, `SELECT url_enc, header_value_enc FROM webhooks WHERE id = ?`, w.ID).Scan(&urlEnc, &headerEnc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(urlEnc, []byte("secret-token")) || bytes.Contains(headerEnc, []byte("s3cret")) {
		t.Error("secrets are stored in the clear")
	}
	if _, err := s.Create(ctx, Input{Name: "chat", URL: ptr("https://other.example")}); !errors.Is(err, ErrTaken) {
		t.Errorf("duplicate name: err = %v, want ErrTaken", err)
	}

	// Nil URL and header value keep the stored ones.
	w, err = s.Update(ctx, w.ID, Input{Name: "chat2", HeaderName: "X-Token", Enabled: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Name != "chat2" || w.Endpoint != "https://hooks.example.com" || w.Enabled {
		t.Errorf("updated = %+v", w)
	}
	targets, err := s.targets(ctx, w.ID, "")
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets = %v, %v", targets, err)
	}
	if got := targets[0]; got.url != "https://hooks.example.com/T0/secret-token" || got.headerValue != "s3cret" {
		t.Errorf("target = %+v", got)
	}
	// The header value doesn't follow the URL to another host, or to another header.
	var inputErr *InputError
	for _, in := range []Input{
		{Name: "chat2", URL: ptr("https://evil.example/x"), HeaderName: "X-Token"},
		{Name: "chat2", HeaderName: "Authorization"},
	} {
		if _, err := s.Update(ctx, w.ID, in); !errors.As(err, &inputErr) {
			t.Errorf("update %+v: err = %v, want an InputError", in, err)
		}
	}
	if w, err = s.Update(ctx, w.ID, Input{Name: "chat2", URL: ptr("https://hooks.example.com/T1/new"), HeaderName: "X-Token"}); err != nil || w.Enabled {
		t.Errorf("new path on the same host, enabled kept: %+v, %v", w, err)
	}
	if targets, _ = s.targets(ctx, w.ID, ""); targets[0].url != "https://hooks.example.com/T1/new" || targets[0].headerValue != "s3cret" {
		t.Errorf("target = %+v", targets[0])
	}
	// Disabled targets get no notifications.
	if all, err := s.targets(ctx, 0, EventExited); err != nil || len(all) != 0 {
		t.Errorf("enabled targets = %v, %v", all, err)
	}

	// No header name drops the header; adding one back needs a value.
	if w, err = s.Update(ctx, w.ID, Input{Name: "chat2", URL: ptr("http://new.example:8080/x")}); err != nil || w.HeaderName != "" || w.Endpoint != "http://new.example:8080" {
		t.Errorf("update = %+v, %v", w, err)
	}
	if _, err := s.Update(ctx, w.ID, Input{Name: "chat2", HeaderName: "X-Token"}); !errors.As(err, &inputErr) {
		t.Errorf("header without a stored value: err = %v", err)
	}
	other, err := s.Create(ctx, Input{Name: "other", URL: ptr("https://other.example")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, other.ID, Input{Name: "chat2"}); !errors.Is(err, ErrTaken) {
		t.Errorf("rename to a taken name: err = %v", err)
	}
	if _, err := s.Update(ctx, 999, Input{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unknown: err = %v", err)
	}

	list, err := s.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "chat2" {
		t.Errorf("list = %+v, %v", list, err)
	}
	if err := s.Delete(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: err = %v", err)
	}
	if _, err := s.Get(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: err = %v", err)
	}
}

func TestMaxWebhooks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	for i := range MaxWebhooks {
		if _, err := s.Create(ctx, Input{Name: "w" + string(rune('a'+i)), URL: ptr("https://x.example")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(ctx, Input{Name: "one too many", URL: ptr("https://x.example")}); !errors.Is(err, ErrTooMany) {
		t.Errorf("err = %v, want ErrTooMany", err)
	}
}

func TestTestDelivery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	h := newHook(t, http.StatusNoContent)
	w, err := s.Create(ctx, Input{Name: "a", URL: ptr(h.URL + "/hook?token=x"), HeaderName: "X-Token", HeaderValue: ptr("s3cret")})
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if err := s.Test(ctx, w.ID); err != nil {
		t.Fatalf("Test = %v", err)
	}
	reqs, bodies := h.received()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.URL.String() != "/hook?token=x" || r.Header.Get("X-Token") != "s3cret" ||
		r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Nudge-Event") != EventTest ||
		r.Header.Get("User-Agent") != "nudge/v1.2.3" {
		t.Errorf("request = %s %s %v", r.Method, r.URL, r.Header)
	}
	if bodies[0].Event != EventTest || !bodies[0].Time.Equal(start.Add(time.Minute)) {
		t.Errorf("body = %+v", bodies[0])
	}
	got, _ := s.Get(ctx, w.ID)
	if got.LastSentAt == nil || !got.LastSentAt.Equal(start.Add(time.Minute)) || got.LastError != "" {
		t.Errorf("after delivery = %+v", got)
	}

	h.mu.Lock()
	h.status = http.StatusInternalServerError
	h.mu.Unlock()
	err = s.Test(ctx, w.ID)
	var derr *DeliveryError
	if !errors.As(err, &derr) || derr.Msg != "the webhook answered with status 500" {
		t.Errorf("Test = %v", err)
	}
	if got, _ := s.Get(ctx, w.ID); got.LastError != derr.Msg {
		t.Errorf("last error = %q", got.LastError)
	}
	if err := s.Test(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown webhook: err = %v", err)
	}
}

func TestDeliveryErrorsHideURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	h := newHook(t, http.StatusOK)
	h.Close()
	w, err := s.Create(ctx, Input{Name: "a", URL: ptr(h.URL + "/secret-token")})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Test(ctx, w.ID)
	if err == nil || strings.Contains(err.Error(), "secret-token") || err.Error() != "could not reach the webhook" {
		t.Errorf("Test = %v", err)
	}
}

func TestNoRedirects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	elsewhere := newHook(t, http.StatusOK)
	redirect := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redirect.Close)
	w, err := s.Create(ctx, Input{Name: "a", URL: ptr(redirect.URL), HeaderName: "X-Token", HeaderValue: ptr("s3cret")})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Test(ctx, w.ID); err == nil || !strings.Contains(err.Error(), "307") {
		t.Errorf("Test = %v, want the redirect status", err)
	}
	if reqs, _ := elsewhere.received(); len(reqs) != 0 {
		t.Error("the redirect was followed")
	}
}

func TestDeliverRetries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	failing := newHook(t, http.StatusBadGateway)
	ok := newHook(t, http.StatusOK)
	off := newHook(t, http.StatusOK)
	rejecting := newHook(t, http.StatusBadRequest)
	for name, in := range map[string]Input{
		"failing":   {URL: ptr(failing.URL), Enabled: ptr(true)},
		"ok":        {URL: ptr(ok.URL), Enabled: ptr(true)},
		"off":       {URL: ptr(off.URL)},
		"rejecting": {URL: ptr(rejecting.URL), Enabled: ptr(true)},
	} {
		in.Name = name
		if _, err := s.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	s.deliverAll(ctx, Message{Event: EventExited, Text: "x"})
	if reqs, _ := failing.received(); len(reqs) != Attempts {
		t.Errorf("failing target got %d attempts, want %d", len(reqs), Attempts)
	}
	if reqs, _ := ok.received(); len(reqs) != 1 {
		t.Errorf("ok target got %d requests", len(reqs))
	}
	if reqs, _ := rejecting.received(); len(reqs) != 1 {
		t.Errorf("a 400 was tried %d times, want once", len(reqs))
	}
	if reqs, _ := off.received(); len(reqs) != 0 {
		t.Errorf("disabled target got %d requests", len(reqs))
	}
	if got, want := clock.Waits(), []time.Duration{RetryDelay, 2 * RetryDelay}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("waits = %v, want %v", got, want)
	}
}

func TestSendDeduplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	send := func() bool {
		t.Helper()
		ok, err := s.Send(ctx, "exited/1/web", time.Hour, Message{Event: EventExited})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !send() {
		t.Error("first send was dropped")
	}
	clock.advance(59 * time.Minute)
	if send() {
		t.Error("repeat within the window was sent")
	}
	clock.advance(time.Minute)
	if !send() {
		t.Error("send after the window was dropped")
	}
	if ok, err := s.Send(ctx, "", 0, Message{}); !ok || err != nil {
		t.Errorf("send without key = %v, %v", ok, err)
	}
	if len(s.queue) != 3 {
		t.Errorf("queued %d, want 3", len(s.queue))
	}
	q := <-s.queue
	if m := q.m; !m.Time.Equal(start) {
		t.Errorf("time = %v, want the clock's", m.Time)
	}

	// Pruning keeps the window of a key that hasn't expired.
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if send() {
		t.Error("prune forgot an unexpired key")
	}
	clock.advance(time.Hour)
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	// An expired key is kept for its recovery until RecoveryWindow has passed too.
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notify_sent`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows after the window = %d, %v", n, err)
	}
	clock.advance(RecoveryWindow)
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notify_sent`).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows after prune = %d, %v", n, err)
	}
}

func TestSendQueueFull(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	for range QueueSize {
		if _, err := s.Send(ctx, "", 0, Message{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Send(ctx, "k", time.Hour, Message{}); !errors.Is(err, ErrQueueFull) {
		t.Errorf("err = %v, want ErrQueueFull", err)
	}
	// The dropped notification's key is released.
	<-s.queue
	if ok, err := s.Send(ctx, "k", time.Hour, Message{}); !ok || err != nil {
		t.Errorf("send after a drop = %v, %v", ok, err)
	}
}

// A notification no webhook got is sent again, rather than quiet for its window.
func TestUndeliveredIsReleased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	results := []updates.Result{{Image: "nginx", Status: updates.Available, RemoteDigest: "sha256:2"}}
	env := environments.Environment{ID: 1, Name: "local"}
	s.UpdatesChecked(ctx, env, results)
	s.deliver(ctx, <-s.queue) // no webhooks yet
	h := newHook(t, http.StatusOK)
	if _, err := s.Create(ctx, Input{Name: "a", URL: ptr(h.URL), Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	s.UpdatesChecked(ctx, env, results)
	if len(s.queue) != 1 {
		t.Fatal("the undelivered update was not sent again")
	}
	s.deliver(ctx, <-s.queue)
	if reqs, _ := h.received(); len(reqs) != 1 {
		t.Fatalf("got %d requests", len(reqs))
	}
	s.UpdatesChecked(ctx, env, results)
	if len(s.queue) != 0 {
		t.Error("a delivered update was sent again")
	}
}

func TestUpdatesChecked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	env := environments.Environment{ID: 4, Name: "edge"}
	results := []updates.Result{
		{Image: "nginx:latest", ImageID: "sha256:a", Status: updates.Available, LocalDigest: "sha256:1", RemoteDigest: "sha256:2"},
		// The same image run as another ID is one update.
		{Image: "nginx:latest", ImageID: "sha256:b", Status: updates.Available, LocalDigest: "sha256:0", RemoteDigest: "sha256:2"},
		{Image: "redis:7", Status: updates.Current, LocalDigest: "sha256:3", RemoteDigest: "sha256:3"},
		{Image: "app:1", Status: updates.Unknown, Error: "registry unreachable"},
	}
	s.UpdatesChecked(ctx, env, results)
	if len(s.queue) != 1 {
		t.Fatalf("queued %d, want 1", len(s.queue))
	}
	m := (<-s.queue).m
	if m.Event != EventUpdateAvailable || m.Environment == nil || m.Environment.Name != "edge" || len(m.Images) != 1 ||
		m.Images[0] != (ImageUpdate{Image: "nginx:latest", LocalDigest: "sha256:1", RemoteDigest: "sha256:2"}) ||
		m.Text != "Update available for nginx:latest on edge" {
		t.Errorf("message = %+v", m)
	}

	// The next check finds the same digest: quiet. A newer one is sent.
	s.UpdatesChecked(ctx, env, results)
	if len(s.queue) != 0 {
		t.Fatal("the same update was sent again")
	}
	results[0].RemoteDigest = "sha256:9"
	results = append(results, updates.Result{Image: "redis:8", Status: updates.Available, LocalDigest: "sha256:5", RemoteDigest: "sha256:6"})
	s.UpdatesChecked(ctx, env, results)
	if len(s.queue) != 1 {
		t.Fatalf("queued %d, want 1", len(s.queue))
	}
	if m := (<-s.queue).m; len(m.Images) != 2 || m.Text != "2 image updates available on edge" {
		t.Errorf("message = %+v", m)
	}
}

func TestRunDelivers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	s, _ := newTestService(t)
	got := make(chan Message, 1)
	h := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var m Message
		_ = json.NewDecoder(r.Body).Decode(&m)
		got <- m
	}))
	t.Cleanup(h.Close)
	if _, err := s.Create(ctx, Input{Name: "a", URL: ptr(h.URL), Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	if _, err := s.Send(ctx, "k", time.Hour, Message{Event: EventAgentOffline, Text: "gone"}); err != nil {
		t.Fatal(err)
	}
	if m := <-got; m.Event != EventAgentOffline || m.Text != "gone" {
		t.Errorf("body = %+v", m)
	}
	cancel()
	<-done
}

// The JSON field names are the contract with the webhook receivers.
func TestMessageJSON(t *testing.T) {
	t.Parallel()
	code := 1
	b, err := json.Marshal(Message{
		Event: EventExited, Time: start, Text: "t", Environment: &Environment{ID: 1, Name: "local"},
		Container: &Container{ID: "abc", Name: "web", Image: "nginx", ExitCode: &code},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event":"container.exited","time":"2023-11-14T22:13:20Z","message":"t","environment":{"id":1,"name":"local"},` +
		`"container":{"id":"abc","name":"web","image":"nginx","exit_code":1}}`
	if string(b) != want {
		t.Errorf("json = %s\nwant   %s", b, want)
	}
}

func TestWebhookEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	problems, recoveries := newHook(t, http.StatusOK), newHook(t, http.StatusOK)
	p, err := s.Create(ctx, Input{Name: "problems", URL: ptr(problems.URL), Enabled: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Events, DefaultEvents) {
		t.Errorf("default events = %v", p.Events)
	}
	r, err := s.Create(ctx, Input{Name: "recoveries", URL: ptr(recoveries.URL), Enabled: ptr(true), Events: []string{EventAgentOnline, EventHealthy, EventHealthy}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{EventHealthy, EventAgentOnline}; !slices.Equal(r.Events, want) {
		t.Errorf("events = %v, want %v", r.Events, want)
	}
	s.deliverAll(ctx, Message{Event: EventHealthy})
	s.deliverAll(ctx, Message{Event: EventExited})
	if _, got := problems.received(); len(got) != 1 || got[0].Event != EventExited {
		t.Errorf("problems got %+v", got)
	}
	if _, got := recoveries.received(); len(got) != 1 || got[0].Event != EventHealthy {
		t.Errorf("recoveries got %+v", got)
	}
	// A test reaches a webhook whatever its events.
	if err := s.Test(ctx, r.ID); err != nil {
		t.Errorf("test: %v", err)
	}

	// Update keeps the events unless it names them.
	if r, err = s.Update(ctx, r.ID, Input{Name: "recoveries"}); err != nil || !slices.Equal(r.Events, []string{EventHealthy, EventAgentOnline}) {
		t.Errorf("update without events = %v, %v", r.Events, err)
	}
	if r, err = s.Update(ctx, r.ID, Input{Name: "recoveries", Events: []string{EventAgentOffline}}); err != nil || !slices.Equal(r.Events, []string{EventAgentOffline}) {
		t.Errorf("update events = %v, %v", r.Events, err)
	}

	// Webhooks from before the events column keep today's events.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO webhooks (name, endpoint, url_enc, header_name, enabled, created_at, updated_at)
		VALUES ('old', 'https://x.example', x'00', '', 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Name != "old" || !slices.Equal(list[0].Events, DefaultEvents) {
		t.Errorf("old webhook = %+v", list[0])
	}
}

func TestRecover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newTestService(t)
	recovery := Message{Event: EventHealthy, Text: "healthy"}
	recoverWeb := func() bool {
		t.Helper()
		ok, err := s.Recover(ctx, "unhealthy/1/web", recovery)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if recoverWeb() {
		t.Error("recovery without a problem was sent")
	}
	if ok, err := s.Send(ctx, "unhealthy/1/web", ProblemWindow, Message{Event: EventUnhealthy}); !ok || err != nil {
		t.Fatalf("send = %v, %v", ok, err)
	}
	<-s.queue
	if !recoverWeb() {
		t.Fatal("recovery after a problem was dropped")
	}
	if q := <-s.queue; q.m.Event != EventHealthy || len(q.keys) != 0 {
		t.Errorf("queued %+v", q)
	}
	if recoverWeb() {
		t.Error("second recovery was sent")
	}
	// The recovery ended the window: the next problem is sent at once.
	if ok, err := s.Send(ctx, "unhealthy/1/web", ProblemWindow, Message{Event: EventUnhealthy}); !ok || err != nil {
		t.Errorf("send after recovery = %v, %v", ok, err)
	}
	<-s.queue
	// A problem that lasts past its window still recovers, for RecoveryWindow.
	clock.advance(ProblemWindow + RecoveryWindow - time.Second)
	if !recoverWeb() {
		t.Error("recovery of a long problem was dropped")
	}
	<-s.queue
	if _, err := s.Send(ctx, "unhealthy/1/web", ProblemWindow, Message{Event: EventUnhealthy}); err != nil {
		t.Fatal(err)
	}
	<-s.queue
	clock.advance(ProblemWindow + RecoveryWindow)
	if recoverWeb() {
		t.Error("recovery after the recovery window was sent")
	}
}

// A notification is posted to MaxDeliveries targets at once.
func TestDeliverConcurrently(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	arrived, release := make(chan struct{}, MaxDeliveries+1), make(chan struct{})
	h := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		arrived <- struct{}{}
		<-release
	}))
	t.Cleanup(h.Close)
	for i := range MaxDeliveries + 1 {
		if _, err := s.Create(ctx, Input{Name: strconv.Itoa(i), URL: ptr(h.URL), Enabled: ptr(true)}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan bool)
	go func() { done <- s.deliverAll(ctx, Message{Event: EventExited}) }()
	for range MaxDeliveries {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("deliveries aren't concurrent")
		}
	}
	select {
	case <-arrived:
		t.Error("more than MaxDeliveries deliveries at once")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if !<-done {
		t.Error("not delivered")
	}
	if len(arrived) != 1 {
		t.Errorf("last delivery: %d arrived, want 1", len(arrived))
	}
}
