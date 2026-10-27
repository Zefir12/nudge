package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

const (
	// defaultExecCmd is the command a session runs when none is asked for.
	defaultExecCmd = "/bin/sh"
	// maxExecCmd bounds the cmd parameter.
	maxExecCmd = 1024
	// maxTermSize bounds a terminal's columns and rows.
	maxTermSize = 1000
	// execChunk is the most output one frame carries.
	execChunk = 32 << 10
	// execInspectTries and execInspectDelay bound waiting for an ended process's exit code.
	execInspectTries = 5
	execInspectDelay = 50 * time.Millisecond
)

// execControl is a text frame from the client: {"type": "resize", "cols": 80, "rows": 24}.
type execControl struct {
	Type string `json:"type"`
	Cols int64  `json:"cols"`
	Rows int64  `json:"rows"`
}

// execEnd is the last frame of an exec session: done with the process's exit code when
// it ended, or an error.
type execEnd struct {
	Done     bool   `json:"done,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// containerExec runs a command with a TTY in a running container and connects it to a
// WebSocket: binary frames carry the terminal's input and output, text frames resize it.
// The session is recorded in the audit log when it opens (or when it is refused). It
// ends when the process exits or the client closes the socket; closing the connection
// to the daemon ends the process's terminal.
func (s *server) containerExec(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w}
	defer func() {
		if rec.status != 0 {
			auditNow(r, rec.status)
		}
	}()
	w = rec

	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	cmd := r.URL.Query().Get("cmd")
	if cmd == "" {
		cmd = defaultExecCmd
	}
	args := strings.Fields(cmd)
	if len(cmd) > maxExecCmd || len(args) == 0 || strings.ContainsRune(cmd, 0) {
		s.writeError(w, http.StatusBadRequest, "cmd must be a command of at most 1024 characters")
		return
	}
	size, ok := s.termSize(w, r)
	if !ok {
		return
	}
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dockerTimeout)
	ct, ref, err := s.ownedContainer(ctx, c, id)
	var execID string
	if err == nil && (ct.State == nil || !ct.State.Running) {
		err = docker.ErrConflict
	}
	if err == nil {
		id = ref
		var res client.ExecCreateResult
		res, err = c.ExecCreate(ctx, id, client.ExecCreateOptions{
			TTY: true, ConsoleSize: size, AttachStdin: true, AttachStdout: true, AttachStderr: true,
			Env: []string{"TERM=xterm-256color"}, Cmd: args,
		})
		execID = res.ID
	}
	cancel()
	switch {
	case docker.NotFound(err):
		s.writeError(w, http.StatusNotFound, "container not found")
		return
	case docker.Conflict(err):
		s.writeError(w, http.StatusConflict, "container is not running")
		return
	case err != nil:
		s.dockerError(w, r, env, "container exec", err)
		return
	}
	s.envs.Seen(r.Context(), &env)

	// Accept refuses an Origin other than the Host, so another site can't open a shell
	// with the user's cookie.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Warn("container exec: websocket upgrade", "env", env.ID, "container", id, "err", err)
		return
	}
	auditNow(r, http.StatusSwitchingProtocols)
	u, _ := userFrom(r.Context())
	s.log.Info("exec session", "env", env.ID, "container", id, "exec", execID, "user", u.Username, "cmd", cmd)
	defer s.log.Info("exec session ended", "env", env.ID, "container", id, "exec", execID)
	s.execSession(r.Context(), ws, c, env.ID, execID, size)
}

// termSize reads the optional initial cols and rows; both or neither must be given.
func (s *server) termSize(w http.ResponseWriter, r *http.Request) (client.ConsoleSize, bool) {
	cols, ok := s.queryInt(w, r, "cols")
	if !ok {
		return client.ConsoleSize{}, false
	}
	rows, ok := s.queryInt(w, r, "rows")
	if !ok {
		return client.ConsoleSize{}, false
	}
	if (cols == 0) != (rows == 0) || cols < 0 || rows < 0 || cols > maxTermSize || rows > maxTermSize {
		s.writeError(w, http.StatusBadRequest, "cols and rows must both be between 1 and 1000")
		return client.ConsoleSize{}, false
	}
	return client.ConsoleSize{Width: uint(cols), Height: uint(rows)}, true
}

// execSession attaches to the exec and pipes it to ws until either side ends.
func (s *server) execSession(parent context.Context, ws *websocket.Conn, c docker.Client, env int64, execID string, size client.ConsoleSize) {
	defer func() { _ = ws.CloseNow() }()
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()

	actx, acancel := context.WithTimeout(ctx, dockerTimeout)
	att, err := c.ExecAttach(actx, execID, client.ExecAttachOptions{TTY: true, ConsoleSize: size})
	acancel()
	if err != nil {
		s.endExec(ctx, ws, c, env, execID, err)
		return
	}
	// Closing the connection ends the process's terminal and the output copy below.
	defer att.Close()
	stop := context.AfterFunc(ctx, att.Close)
	defer stop()
	wg.Go(func() { keepAlive(ctx, cancel, ws) })
	wg.Go(func() { s.execInput(ctx, cancel, ws, c, env, execID, att.Conn) })

	buf := make([]byte, execChunk)
	for {
		n, rerr := att.Reader.Read(buf)
		if n > 0 {
			if err := ws.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
				return
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				rerr = nil
			}
			s.endExec(ctx, ws, c, env, execID, rerr)
			return
		}
	}
}

// execInput sends the client's binary frames to the process and applies its resizes,
// until the client closes the socket; then it ends the session.
func (s *server) execInput(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, c docker.Client, env int64, execID string, conn net.Conn) {
	defer cancel()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if _, err := conn.Write(data); err != nil {
				return
			}
			continue
		}
		var m execControl
		if json.Unmarshal(data, &m) != nil || m.Type != "resize" ||
			m.Cols < 1 || m.Rows < 1 || m.Cols > maxTermSize || m.Rows > maxTermSize {
			continue
		}
		rctx, rcancel := context.WithTimeout(ctx, dockerTimeout)
		_, err = c.ExecResize(rctx, execID, client.ExecResizeOptions{Width: uint(m.Cols), Height: uint(m.Rows)})
		rcancel()
		if err != nil && ctx.Err() == nil {
			s.log.Warn("container exec: resize", "env", env, "exec", execID, "err", err)
		}
	}
}

// execExitCode asks for the exit code of a process whose output ended; the daemon may
// report it running for a moment longer, so it asks a few times. Nil when it can't tell.
func (s *server) execExitCode(ctx context.Context, c docker.Client, execID string) *int {
	for try := 0; try < execInspectTries; try++ {
		if try > 0 {
			t := time.NewTimer(execInspectDelay)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil
			case <-t.C:
			}
		}
		ictx, icancel := context.WithTimeout(ctx, dockerTimeout)
		info, err := c.ExecInspect(ictx, execID, client.ExecInspectOptions{})
		icancel()
		if err != nil {
			return nil
		}
		if !info.Running {
			return &info.ExitCode
		}
	}
	return nil
}

// endExec sends the last frame of a session, with the exit code when the process ended,
// and closes the socket, unless the client is gone.
func (s *server) endExec(ctx context.Context, ws *websocket.Conn, c docker.Client, env int64, execID string, err error) {
	if ctx.Err() != nil {
		return
	}
	end := execEnd{Done: true}
	if err != nil {
		// The daemon's message stays in the log; it can carry secrets.
		s.log.Warn("container exec", "env", env, "exec", execID, "err", err)
		end = execEnd{Error: "the environment's Docker host ended the exec session with an error"}
	} else {
		end.ExitCode = s.execExitCode(ctx, c, execID)
	}
	if wsjson.Write(ctx, ws, end) == nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}
}
