package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	mevents "github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func newEventsTest(t *testing.T) (logsTest, chan mevents.Message, chan error) {
	t.Helper()
	msgs, errs := make(chan mevents.Message), make(chan error, 1)
	fake := &dockertest.Fake{EventsResult: client.EventsResult{Messages: msgs, Err: errs}}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
	srv := httptest.NewServer(et.h)
	t.Cleanup(srv.Close)
	return logsTest{envTest: et, srv: srv, fake: fake}, msgs, errs
}

func (lt logsTest) eventsPath(query string) string {
	return "/api/v1/environments/" + strconv.FormatInt(lt.ids["local"], 10) + "/events" + query
}

func TestEnvironmentEventsErrors(t *testing.T) {
	t.Parallel()
	lt, _, _ := newEventsTest(t)
	tests := []struct {
		name, path string
		cookie     bool
		code       int
	}{
		{"no session", lt.eventsPath(""), false, http.StatusUnauthorized},
		{"unknown type", lt.eventsPath("?type=container,secret"), true, http.StatusBadRequest},
		{"unknown environment", "/api/v1/environments/999/events", true, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := request{method: "GET", path: tt.path}
			if tt.cookie {
				req.cookie = lt.cookie
			}
			if rec := do(t, lt.h, req); rec.Code != tt.code {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.code, rec.Body)
			}
		})
	}
}

func TestEnvironmentEventsStream(t *testing.T) {
	t.Parallel()
	lt, msgs, errs := newEventsTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	all, err := lt.dial(ctx, t, lt.eventsPath(""))
	if err != nil {
		t.Fatal(err)
	}
	images, err := lt.dial(ctx, t, lt.eventsPath("?type=image&type=volume"))
	if err != nil {
		t.Fatal(err)
	}

	msgs <- mevents.Message{Type: "container", Action: "start", Time: 1_800_000_000, Actor: mevents.Actor{ID: "abc", Attributes: map[string]string{"name": "web"}}}
	msgs <- mevents.Message{Type: "image", Action: "pull", Time: 1_800_000_001, Actor: mevents.Actor{ID: "nginx:latest"}}
	errs <- errors.New("daemon: secret-detail")

	end := `{"error":"the environment's Docker host ended the event stream"}`
	for _, tt := range []struct {
		name string
		ws   *websocket.Conn
		want string
	}{
		{"all", all, `[{"action":"start","attributes":{"name":"web"},"id":"abc","name":"web","time":"2027-01-15T08:00:00Z","type":"container"},` +
			`{"action":"pull","id":"nginx:latest","time":"2027-01-15T08:00:01Z","type":"image"},` + end + `]`},
		{"filtered", images, `[{"action":"pull","id":"nginx:latest","time":"2027-01-15T08:00:01Z","type":"image"},` + end + `]`},
	} {
		got, err := json.Marshal(readAll(ctx, t, tt.ws))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tt.want {
			t.Errorf("%s: frames\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}
	if n := lt.fake.EventStreams(); n != 1 {
		t.Errorf("docker event streams = %d, want 1 shared by both clients", n)
	}
}
