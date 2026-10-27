package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
)

// eventTypes are the Docker object types a client can filter events by.
var eventTypes = map[string]bool{
	"container": true, "image": true, "volume": true, "network": true, "daemon": true, "plugin": true,
}

// environmentEvents streams an environment's Docker events over a WebSocket, all of them
// or only the types asked for (type=container,image or repeated type params). Every
// client of an environment shares one Docker stream; a client that falls behind is sent
// an error frame and disconnected, and so is every client when the Docker stream ends.
func (s *server) environmentEvents(w http.ResponseWriter, r *http.Request) {
	types := map[string]bool{}
	for _, v := range r.URL.Query()["type"] {
		for t := range strings.SplitSeq(v, ",") {
			if !eventTypes[t] {
				s.writeError(w, http.StatusBadRequest, "type must be container, image, volume, network, daemon or plugin")
				return
			}
			types[t] = true
		}
	}
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	sc, err := s.scopeOf(r.Context())
	if err != nil {
		s.dockerError(w, r, env, "events", err)
		return
	}
	owners := &ownerCache{log: s.log, sc: sc, c: c, owners: map[string]bool{}}
	sub, err := s.events.Subscribe(env.ID, c)
	if errors.Is(err, events.ErrTooMany) {
		s.writeError(w, http.StatusServiceUnavailable, "too many event streams open for this environment")
		return
	}
	if err != nil {
		s.internalError(w, "subscribe to events", err)
		return
	}
	defer sub.Close()

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Warn("events: websocket upgrade", "env", env.ID, "err", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx, cancel := context.WithCancel(ws.CloseRead(r.Context()))
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	wg.Go(func() { keepAlive(ctx, cancel, ws) })

	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.C():
			if !ok {
				s.endEvents(ctx, ws, sub.Err())
				return
			}
			if len(types) > 0 && !types[e.Type] || !owners.event(ctx, e.Type, e.ID, e.Attributes) {
				continue
			}
			if wsjson.Write(ctx, ws, e) != nil {
				return
			}
		}
	}
}

// endEvents sends the error frame of an event stream the hub ended and closes the socket.
func (s *server) endEvents(ctx context.Context, ws *websocket.Conn, err error) {
	msg := "the environment's Docker host ended the event stream"
	if errors.Is(err, events.ErrSlow) {
		msg = "the event stream fell behind; reconnect to resume"
	}
	if wsjson.Write(ctx, ws, streamEnd{Error: msg}) == nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}
}
