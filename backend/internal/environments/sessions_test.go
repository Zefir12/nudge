package environments

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel/tunneltest"
)

// newSession returns the server side of a tunnel to a fake agent that answers every
// request with h.
func newSession(t *testing.T, h http.Handler) (*tunnel.Session, *tunneltest.Agent) {
	t.Helper()
	accepted := make(chan *tunnel.Session, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket accept: %v", err)
			close(accepted)
			return
		}
		sess, err := tunnel.Accept(context.Background(), ws, tunnel.Hello{Version: "server"}, tunnel.Config{})
		if err != nil {
			t.Errorf("tunnel accept: %v", err)
			close(accepted)
			return
		}
		accepted <- sess
		<-sess.Done()
	}))
	t.Cleanup(srv.Close)
	agent, err := tunneltest.Connect(t, srv.URL, nil, tunnel.Hello{Version: "v2.0.0", Hostname: "nas"}, h)
	if err != nil {
		t.Fatal(err)
	}
	sess := <-accepted
	if sess == nil {
		t.FailNow()
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, agent
}

// newEnrolled returns an agent environment with credentials.
func newEnrolled(t *testing.T, s *Service) (Environment, Credentials) {
	t.Helper()
	ctx := context.Background()
	env, jt, err := s.CreateAgent(ctx, "nas")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := s.Enroll(ctx, jt.Token)
	if err != nil {
		t.Fatal(err)
	}
	return env, creds
}

// serve runs ServeAgent in the background; the channel gets its result.
func serve(s *Service, env Environment, agentID string, sess *tunnel.Session) <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- s.ServeAgent(context.Background(), env, agentID, "192.0.2.7", sess) }()
	return errc
}

func wait(t *testing.T, errc <-chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("ServeAgent did not return")
		return nil
	}
}

var pingOK = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Api-Version", client.MaxAPIVersion)
	_, _ = w.Write([]byte("OK"))
})

func TestServeAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock, connects := newTestService(t, &dockertest.Fake{})
	env, creds := newEnrolled(t, s)

	if s.Online(ctx, &env) {
		t.Fatal("online without a tunnel")
	}
	c, err := s.Client(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ping(ctx, client.PingOptions{}); !docker.Unreachable(err) || !errors.Is(err, ErrAgentOffline) {
		t.Errorf("Ping without a tunnel = %v, want unreachable ErrAgentOffline", err)
	}

	sess, _ := newSession(t, pingOK)
	errc := serve(s, env, creds.AgentID, sess)
	// Online pings through the tunnel once ServeAgent registered it.
	deadline := time.Now().Add(5 * time.Second)
	for !s.Online(ctx, &env) {
		if time.Now().After(deadline) {
			t.Fatal("not online with a tunnel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if connects() != 0 {
		t.Errorf("Connect called %d times, want agent environments to use their tunnel", connects())
	}
	got, err := s.Get(ctx, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentVersion != "v2.0.0" || got.LastSeenAt == nil {
		t.Errorf("after connect: version %q, last seen %v", got.AgentVersion, got.LastSeenAt)
	}

	clock.Advance(time.Hour)
	_ = sess.Close()
	if err := wait(t, errc); err != nil {
		t.Fatalf("ServeAgent = %v", err)
	}
	got, err = s.Get(ctx, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeenAt == nil || !got.LastSeenAt.Equal(clock.Now()) {
		t.Errorf("last seen = %v, want the disconnect time %v", got.LastSeenAt, clock.Now())
	}
	if s.Online(ctx, &env) {
		t.Error("online after the tunnel closed")
	}
}

func TestServeAgentRefuses(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t, &dockertest.Fake{})
	env, creds := newEnrolled(t, s)
	tests := []struct {
		name    string
		env     Environment
		agentID string
		wantErr error
	}{
		{"local environment", Environment{ID: env.ID, Kind: KindLocal}, creds.AgentID, ErrNotAgent},
		// Credentials replaced between authentication and registration.
		{"stale agent ID", env, "agt_stale", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess, agent := newSession(t, pingOK)
			if err := wait(t, serve(s, tt.env, tt.agentID, sess)); !errors.Is(err, tt.wantErr) {
				t.Errorf("ServeAgent = %v, want %v", err, tt.wantErr)
			}
			select {
			case <-agent.Session.Done():
			case <-time.After(5 * time.Second):
				t.Error("tunnel still open")
			}
		})
	}
}

func TestCloseDropsSessions(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t, &dockertest.Fake{})
	env, creds := newEnrolled(t, s)
	sess, _ := newSession(t, pingOK)
	errc := serve(s, env, creds.AgentID, sess)
	deadline := time.Now().Add(5 * time.Second)
	for !s.Online(context.Background(), &env) {
		if time.Now().After(deadline) {
			t.Fatal("not online with a tunnel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Close waited for ServeAgent.
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("ServeAgent = %v", err)
		}
	default:
		t.Error("Close returned before ServeAgent")
	}
	late, _ := newSession(t, pingOK)
	if err := wait(t, serve(s, env, creds.AgentID, late)); !errors.Is(err, ErrClosed) {
		t.Errorf("ServeAgent after Close = %v, want ErrClosed", err)
	}
}

func TestServeAgentLink(t *testing.T) {
	t.Parallel()
	s, clock, _ := newTestService(t, &dockertest.Fake{})
	waits := make(chan time.Duration, 10)
	ticks := make(chan time.Time)
	s.after = func(d time.Duration) <-chan time.Time {
		waits <- d
		return ticks
	}
	env, creds := newEnrolled(t, s)
	if _, ok := s.Link(env.ID); ok {
		t.Fatal("link without a tunnel")
	}
	waitPing := func() {
		t.Helper()
		select {
		case d := <-waits:
			if d != pingInterval {
				t.Errorf("waits %v between pings, want %v", d, pingInterval)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("tunnel not pinged")
		}
	}

	sess, _ := newSession(t, pingOK)
	errc := serve(s, env, creds.AgentID, sess)
	// The first ping goes out at connect.
	waitPing()
	link, ok := s.Link(env.ID)
	if !ok || link.Addr != "192.0.2.7" || link.RTT <= 0 || !link.PingedAt.Equal(clock.Now()) {
		t.Errorf("after connect: link %+v, %v; want 192.0.2.7 with a round trip at %v", link, ok, clock.Now())
	}

	clock.Advance(pingInterval)
	ticks <- clock.Now()
	waitPing()
	if link, _ := s.Link(env.ID); !link.PingedAt.Equal(clock.Now()) {
		t.Errorf("after a tick: pinged at %v, want %v", link.PingedAt, clock.Now())
	}

	_ = sess.Close()
	if err := wait(t, errc); err != nil {
		t.Fatalf("ServeAgent = %v", err)
	}
	if link, ok := s.Link(env.ID); ok {
		t.Errorf("link after disconnect: %+v", link)
	}
}
