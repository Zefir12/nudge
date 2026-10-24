package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

const (
	// logPingInterval keeps a quiet log stream alive through proxies that close idle
	// connections; a ping not answered within logPingTimeout ends the stream.
	logPingInterval = 30 * time.Second
	logPingTimeout  = 10 * time.Second
	// maxLogLine splits longer lines, as the daemon does, so a stream without newlines
	// can't grow a buffer without bound and frames fit clients' default read limits.
	maxLogLine = 16 << 10
)

// logLine is one frame of a log stream: a line without its newline, from stdout or
// stderr, with the daemon's timestamp when asked for.
type logLine struct {
	Stream string     `json:"stream"`
	Time   *time.Time `json:"time,omitempty"`
	Line   string     `json:"line"`
}

// logEnd is the last frame: done when the stream ended (the container stopped or the
// lines asked for were sent), or an error.
type logEnd struct {
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}

// containerLogs streams a container's logs over a WebSocket. The options and the container
// are checked before the upgrade, so those failures are plain JSON errors. The Docker
// stream runs on a context that ends when the client closes the socket or goes away.
func (s *server) containerLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	follow, ok := s.queryBool(w, r, "follow")
	if !ok {
		return
	}
	timestamps, ok := s.queryBool(w, r, "timestamps")
	if !ok {
		return
	}
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "all"
	}
	if n, err := strconv.Atoi(tail); tail != "all" && (err != nil || n < 0) {
		s.writeError(w, http.StatusBadRequest, "tail must be a non-negative integer or all")
		return
	}
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	ictx, icancel := context.WithTimeout(r.Context(), dockerTimeout)
	info, err := c.ContainerInspect(ictx, id, client.ContainerInspectOptions{})
	icancel()
	if docker.NotFound(err) {
		s.writeError(w, http.StatusNotFound, "container not found")
		return
	}
	if err != nil {
		s.dockerError(w, r, env, "container logs", err)
		return
	}
	s.envs.Seen(r.Context(), &env)
	tty := info.Container.Config != nil && info.Container.Config.Tty

	// Accept answers a failed upgrade itself; it refuses an Origin other than the Host,
	// so another site can't open the stream with the user's cookie.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Warn("container logs: websocket upgrade", "env", env.ID, "container", id, "err", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx, cancel := context.WithCancel(ws.CloseRead(r.Context()))
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	wg.Go(func() { keepAlive(ctx, cancel, ws) })

	logs, err := c.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: follow, Timestamps: timestamps, Tail: tail,
	})
	if err != nil {
		s.endLogs(ctx, ws, env.ID, id, err)
		return
	}
	defer func() { _ = logs.Close() }()

	out := &lineWriter{ctx: ctx, ws: ws, stream: "stdout", timestamps: timestamps}
	errOut := &lineWriter{ctx: ctx, ws: ws, stream: "stderr", timestamps: timestamps}
	if tty {
		_, err = io.Copy(out, logs)
	} else {
		_, err = stdcopy.StdCopy(out, errOut, logs)
	}
	if err == nil {
		err = out.flush()
	}
	if err == nil {
		err = errOut.flush()
	}
	s.endLogs(ctx, ws, env.ID, id, err)
}

// endLogs sends the last frame and closes the socket, unless the client is gone.
func (s *server) endLogs(ctx context.Context, ws *websocket.Conn, env int64, id string, err error) {
	if ctx.Err() != nil || errors.Is(err, errClientGone) {
		return
	}
	end := logEnd{Done: true}
	if err != nil {
		// The daemon's message stays in the log; it can carry secrets.
		s.log.Warn("container logs", "env", env, "container", id, "err", err)
		end = logEnd{Error: "the environment's Docker host ended the log stream with an error"}
	}
	if wsjson.Write(ctx, ws, end) == nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}
}

// keepAlive pings the client until ctx is done and cancels the stream when a ping goes
// unanswered.
func keepAlive(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn) {
	t := time.NewTicker(logPingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, logPingTimeout)
			err := ws.Ping(pctx)
			pcancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// lineWriter splits what one stream writes into lines and sends each as a frame.
type lineWriter struct {
	ctx        context.Context
	ws         *websocket.Conn
	stream     string
	timestamps bool
	buf        []byte
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	b := append(lw.buf, p...)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		switch {
		case i >= 0 && i <= maxLogLine:
			if err := lw.send(b[:i]); err != nil {
				return 0, err
			}
			b = b[i+1:]
		case len(b) >= maxLogLine:
			n := splitAt(b)
			if err := lw.send(b[:n]); err != nil {
				return 0, err
			}
			b = b[n:]
		default:
			// A partial line waits for the rest.
			lw.buf = append(lw.buf[:0], b...)
			return len(p), nil
		}
	}
	lw.buf = lw.buf[:0]
	return len(p), nil
}

// splitAt returns where to split a line over maxLogLine: at the limit, moved back to the
// start of a UTF-8 sequence so no character is cut in two.
func splitAt(b []byte) int {
	for n := maxLogLine; n > maxLogLine-utf8.UTFMax; n-- {
		if utf8.RuneStart(b[n]) {
			return n
		}
	}
	return maxLogLine
}

// flush sends a last line that had no newline.
func (lw *lineWriter) flush() error {
	if len(lw.buf) == 0 {
		return nil
	}
	err := lw.send(lw.buf)
	lw.buf = lw.buf[:0]
	return err
}

func (lw *lineWriter) send(b []byte) error {
	line := logLine{Stream: lw.stream, Line: strings.TrimSuffix(string(b), "\r")}
	if lw.timestamps {
		// The daemon prefixes each line with an RFC 3339 time and a space.
		if ts, rest, ok := strings.Cut(line.Line, " "); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				t = t.UTC()
				line.Time, line.Line = &t, rest
			}
		}
	}
	if err := wsjson.Write(lw.ctx, lw.ws, line); err != nil {
		return errClientGone
	}
	return nil
}

// errClientGone ends a copy whose frames can't be sent any more.
var errClientGone = errors.New("log stream client gone")
