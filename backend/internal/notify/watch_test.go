package notify

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	mevents "github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
)

// fakeEnvs lists envs with the online state and Docker clients the test sets.
type fakeEnvs struct {
	mu      sync.Mutex
	envs    []environments.Environment
	online  map[int64]bool
	clients map[int64]*dockertest.Fake
}

func (f *fakeEnvs) List(context.Context) ([]environments.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]environments.Environment(nil), f.envs...), nil
}

func (f *fakeEnvs) Client(env environments.Environment) (docker.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.clients[env.ID]; c != nil {
		return c, nil
	}
	return nil, errors.New("no client")
}

func (f *fakeEnvs) Online(_ context.Context, env *environments.Environment) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[env.ID]
}

func (f *fakeEnvs) setOnline(id int64, online bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online[id] = online
}

type sent struct {
	key    string
	window time.Duration
	m      Message
	// recovery is set for Recover.
	recovery bool
}

// fakeSender records the sends and recoveries, and fails them with err.
type fakeSender struct {
	c   chan sent
	err error
}

func (f *fakeSender) Send(_ context.Context, key string, window time.Duration, m Message) (bool, error) {
	f.c <- sent{key: key, window: window, m: m}
	return f.err == nil, f.err
}

func (f *fakeSender) Recover(_ context.Context, key string, m Message) (bool, error) {
	f.c <- sent{key: key, m: m, recovery: true}
	return f.err == nil, f.err
}

// next returns the next send, failing the test if there is none.
func (f *fakeSender) next(t *testing.T) sent {
	t.Helper()
	select {
	case s := <-f.c:
		return s
	default:
		t.Fatal("nothing sent")
		return sent{}
	}
}

func (f *fakeSender) none(t *testing.T) {
	t.Helper()
	select {
	case s := <-f.c:
		t.Errorf("unexpected notification %+v", s)
	default:
	}
}

func newTestWatcher(envs *fakeEnvs) (*Watcher, *fakeSender, *fakeClock) {
	send := &fakeSender{c: make(chan sent, 10)}
	clock := &fakeClock{hold: map[time.Duration]bool{WatchInterval: true}}
	w := NewWatcher(WatchOptions{Envs: envs, Hub: events.New(events.Options{Log: slog.New(slog.DiscardHandler)}), Send: send, Clock: clock})
	return w, send, clock
}

func TestHandleContainerEvents(t *testing.T) {
	t.Parallel()
	env := environments.Environment{ID: 2, Name: "local"}
	die := func(id, code string) events.Event {
		return events.Event{Type: "container", Action: "die", ID: id, Name: "web", Attributes: map[string]string{"exitCode": code, "image": "nginx"}}
	}
	kill := events.Event{Type: "container", Action: "kill", ID: "c1", Name: "web"}
	tests := []struct {
		name     string
		events   []events.Event
		wait     time.Duration // between the first event and the others
		wantKey  string
		wantCode int
		// wantEvent is the event without an exit code; empty means EventUnhealthy.
		wantEvent string
	}{
		{name: "crash", events: []events.Event{die("c1", "1")}, wantKey: "exited/2/web", wantCode: 1},
		{name: "clean exit", events: []events.Event{die("c1", "0")}},
		{name: "stopped", events: []events.Event{kill, die("c1", "143")}},
		{name: "crash after a reload signal", events: []events.Event{{Type: "container", Action: "kill", ID: "c1", Attributes: map[string]string{"signal": "1"}}, die("c1", "1")}, wantKey: "exited/2/web", wantCode: 1},
		{name: "kill of another container", events: []events.Event{{Type: "container", Action: "kill", ID: "c2"}, die("c1", "137")}, wantKey: "exited/2/web", wantCode: 137},
		{name: "crash long after a signal", events: []events.Event{kill, die("c1", "2")}, wait: killWindow + time.Second, wantKey: "exited/2/web", wantCode: 2},
		{name: "no exit code", events: []events.Event{die("c1", "")}},
		{name: "unhealthy", events: []events.Event{{Type: "container", Action: "health_status: unhealthy", ID: "c1", Name: "web"}}, wantKey: "unhealthy/2/web"},
		{name: "healthy", events: []events.Event{{Type: "container", Action: "health_status: healthy", ID: "c1", Name: "web"}}, wantKey: "unhealthy/2/web", wantEvent: EventHealthy},
		{name: "unnamed", events: []events.Event{{Type: "container", Action: "die", ID: "c1", Attributes: map[string]string{"exitCode": "1"}}}, wantKey: "exited/2/c1", wantCode: 1},
		{name: "not a container", events: []events.Event{{Type: "network", Action: "die", ID: "n", Attributes: map[string]string{"exitCode": "1"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w, send, clock := newTestWatcher(&fakeEnvs{})
			killed := map[string]time.Time{}
			for i, e := range tt.events {
				if i == 1 {
					clock.advance(tt.wait)
				}
				w.handle(context.Background(), env, killed, e)
			}
			if tt.wantKey == "" {
				send.none(t)
				return
			}
			select {
			case s := <-send.c:
				if s.key != tt.wantKey || s.m.Environment == nil || s.m.Environment.ID != 2 || s.m.Container == nil {
					t.Fatalf("sent %+v", s)
				}
				if tt.wantCode != 0 && (s.m.Event != EventExited || s.m.Container.ExitCode == nil || *s.m.Container.ExitCode != tt.wantCode) {
					t.Errorf("sent %+v, want exit code %d", s.m, tt.wantCode)
				}
				wantEvent := cmp.Or(tt.wantEvent, EventUnhealthy)
				if tt.wantCode == 0 && s.m.Event != wantEvent {
					t.Errorf("event = %s, want %s", s.m.Event, wantEvent)
				}
				if s.recovery != (s.m.Event == EventHealthy) {
					t.Errorf("recovery = %v for %s", s.recovery, s.m.Event)
				}
			default:
				t.Fatal("nothing sent")
			}
			send.none(t)
		})
	}
}

func TestKilledIsBounded(t *testing.T) {
	t.Parallel()
	w, _, _ := newTestWatcher(&fakeEnvs{})
	killed := map[string]time.Time{}
	for i := range maxKilled + 10 {
		w.handle(context.Background(), environments.Environment{ID: 1}, killed, events.Event{Type: "container", Action: "kill", ID: string(rune(i + 'A'))})
	}
	if len(killed) > maxKilled {
		t.Errorf("killed has %d entries, want at most %d", len(killed), maxKilled)
	}
}

func TestAgentOffline(t *testing.T) {
	t.Parallel()
	seen := start.Add(-time.Hour)
	envs := &fakeEnvs{
		envs: []environments.Environment{
			{ID: 1, Name: "edge", Kind: environments.KindAgent, Enrolled: true, LastSeenAt: &seen},
			// Never connected: nothing to report.
			{ID: 2, Name: "new", Kind: environments.KindAgent, Enrolled: true},
			{ID: 3, Name: "pending", Kind: environments.KindAgent},
		},
		online: map[int64]bool{},
	}
	w, send, clock := newTestWatcher(envs)
	ctx := context.Background()
	look := func(d time.Duration) {
		clock.advance(d)
		w.look(ctx)
	}
	look(0)
	look(OfflineGrace - time.Second)
	send.none(t)
	look(time.Second)
	if s := send.next(t); s.key != "offline/1" || s.window != OfflineWindow || s.recovery || s.m.Event != EventAgentOffline || s.m.Environment.Name != "edge" {
		t.Errorf("sent %+v", s)
	}
	look(OfflineGrace)
	send.none(t)
	// Still offline a day later: notified again.
	look(OfflineWindow - OfflineGrace)
	if s := send.next(t); s.key != "offline/1" || s.m.Event != EventAgentOffline {
		t.Errorf("sent %+v", s)
	}
	send.none(t)

	// Back online: a recovery, and the next outage is notified after its own grace.
	envs.setOnline(1, true)
	look(time.Second)
	if s := send.next(t); s.key != "offline/1" || !s.recovery || s.m.Event != EventAgentOnline || s.m.Environment.ID != 1 {
		t.Errorf("sent %+v", s)
	}
	look(time.Second)
	send.none(t)
	envs.setOnline(1, false)
	look(time.Second)
	look(OfflineGrace - time.Second)
	send.none(t)
	look(time.Second)
	if s := send.next(t); s.m.Event != EventAgentOffline {
		t.Errorf("second outage: sent %+v", s)
	}

	// An outage shorter than the grace has no recovery either.
	envs.setOnline(1, true)
	look(time.Second)
	send.next(t)
	envs.setOnline(1, false)
	look(time.Second)
	envs.setOnline(1, true)
	look(time.Second)
	send.none(t)

	// Removed environments are forgotten.
	envs.mu.Lock()
	envs.envs = nil
	envs.mu.Unlock()
	look(0)
	if len(w.agents) != 0 {
		t.Errorf("agents = %v", w.agents)
	}
}

// A notification or recovery that couldn't be queued is tried again at the next look.
func TestAgentRetriesFailedSends(t *testing.T) {
	t.Parallel()
	seen := start
	envs := &fakeEnvs{
		envs:   []environments.Environment{{ID: 1, Name: "edge", Kind: environments.KindAgent, Enrolled: true, LastSeenAt: &seen}},
		online: map[int64]bool{},
	}
	w, send, clock := newTestWatcher(envs)
	ctx := context.Background()
	w.look(ctx)
	clock.advance(OfflineGrace)
	send.err = ErrQueueFull
	w.look(ctx)
	send.next(t)
	send.err = nil
	w.look(ctx)
	if s := send.next(t); s.m.Event != EventAgentOffline {
		t.Errorf("retry sent %+v", s)
	}
	w.look(ctx)
	send.none(t)

	envs.setOnline(1, true)
	send.err = ErrQueueFull
	w.look(ctx)
	send.next(t)
	send.err = nil
	w.look(ctx)
	if s := send.next(t); s.m.Event != EventAgentOnline {
		t.Errorf("retry sent %+v", s)
	}
	w.look(ctx)
	send.none(t)
}

func TestAgentOnlineAtStart(t *testing.T) {
	t.Parallel()
	seen := start
	envs := &fakeEnvs{
		envs:   []environments.Environment{{ID: 1, Name: "edge", Kind: environments.KindAgent, Enrolled: true, LastSeenAt: &seen}},
		online: map[int64]bool{1: true},
	}
	w, send, _ := newTestWatcher(envs)
	// An outage notified before a restart may still need its recovery.
	w.look(context.Background())
	if s := send.next(t); s.key != "offline/1" || !s.recovery {
		t.Errorf("sent %+v", s)
	}
	w.look(context.Background())
	send.none(t)
	w.stop()
}

// TestPingsConcurrently checks that environments are pinged at once, up to MaxPings.
func TestPingsConcurrently(t *testing.T) {
	t.Parallel()
	envs := &blockingEnvs{release: make(chan struct{}), started: make(chan struct{}, 2*MaxPings)}
	for i := range 2 * MaxPings {
		envs.envs = append(envs.envs, environments.Environment{ID: int64(i + 1), Kind: environments.KindLocal})
	}
	w, _, _ := newTestWatcher(&fakeEnvs{})
	w.envs = envs
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.look(context.Background())
	}()
	for range MaxPings {
		<-envs.started
	}
	select {
	case <-envs.started:
		t.Error("more than MaxPings pings at once")
	case <-time.After(50 * time.Millisecond):
	}
	close(envs.release)
	<-done
	if n := len(envs.started); n != MaxPings {
		t.Errorf("%d pings after the first %d, want %d", n, MaxPings, MaxPings)
	}
}

// blockingEnvs holds every ping until release is closed; all are offline.
type blockingEnvs struct {
	envs    []environments.Environment
	started chan struct{}
	release chan struct{}
}

func (b *blockingEnvs) List(context.Context) ([]environments.Environment, error) { return b.envs, nil }

func (b *blockingEnvs) Client(environments.Environment) (docker.Client, error) {
	return nil, errors.New("no client")
}

func (b *blockingEnvs) Online(context.Context, *environments.Environment) bool {
	b.started <- struct{}{}
	<-b.release
	return false
}

func TestWatchStreams(t *testing.T) {
	t.Parallel()
	msgs, errs := make(chan mevents.Message), make(chan error, 1)
	fake := &dockertest.Fake{EventsResult: client.EventsResult{Messages: msgs, Err: errs}}
	envs := &fakeEnvs{
		envs:    []environments.Environment{{ID: 1, Name: "local", Kind: environments.KindLocal}, {ID: 2, Name: "down", Kind: environments.KindLocal}},
		online:  map[int64]bool{1: true},
		clients: map[int64]*dockertest.Fake{1: fake, 2: {}},
	}
	w, send, _ := newTestWatcher(envs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	msgs <- mevents.Message{Type: "container", Action: "die", Actor: mevents.Actor{ID: "c1", Attributes: map[string]string{"name": "web", "exitCode": "3"}}}
	if s := <-send.c; s.key != "exited/1/web" {
		t.Errorf("sent %+v", s)
	}
	if n := envs.clients[2].EventStreams(); n != 0 {
		t.Errorf("offline environment: %d event streams", n)
	}
	cancel()
	<-done
	if n := fake.EventStreams(); n != 1 {
		t.Errorf("event streams = %d, want 1", n)
	}
}

func TestWatchRestartsEndedStream(t *testing.T) {
	t.Parallel()
	errs := make(chan error, 1)
	fake := &dockertest.Fake{EventsResult: client.EventsResult{Err: errs}}
	envs := &fakeEnvs{
		envs:    []environments.Environment{{ID: 1, Name: "local", Kind: environments.KindLocal}},
		online:  map[int64]bool{1: true},
		clients: map[int64]*dockertest.Fake{1: fake},
	}
	w, _, _ := newTestWatcher(envs)
	ctx := context.Background()
	w.look(ctx)
	st := w.streams[1]
	errs <- errors.New("daemon restarted")
	<-st.done
	w.look(ctx)
	// The hub opens the new stream in its own goroutine.
	for deadline := time.Now().Add(5 * time.Second); fake.EventStreams() != 2; {
		if time.Now().After(deadline) {
			t.Fatalf("event streams = %d, want 2", fake.EventStreams())
		}
		time.Sleep(time.Millisecond)
	}
	// Deleted environments lose their stream.
	envs.mu.Lock()
	envs.envs = nil
	envs.mu.Unlock()
	w.look(ctx)
	if len(w.streams) != 0 {
		t.Errorf("streams = %v", w.streams)
	}
	w.stop()
}
