package stacks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode"
)

// The control service is how the server deploys stacks on an agent: HTTP/1.1 over the
// tunnel's control streams, served by the agent with NewControl and called by the server
// through a Remote runner. docs/architecture.md (Agent control protocol) has the design.

// maxControlBody bounds a control request body: a stack's files, or a few flags.
const maxControlBody = 2 << 20

// maxEventSize bounds one line of a control output stream (an output line is at most
// maxLine bytes, which JSON escaping can grow).
const maxEventSize = 1 << 20

// ErrBusy means the agent is already changing or deploying the stack.
var ErrBusy = errors.New("the stack is being changed on the agent")

// controlEvent is one line of a control output stream: an output line, then the exit code
// or an error.
type controlEvent struct {
	Stream   string `json:"stream,omitempty"`
	Line     string `json:"line,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

type controlFiles struct {
	Compose string `json:"compose"`
	Env     string `json:"env"`
}

// control serves the control endpoints on a Runner.
type control struct {
	runner Runner
	log    *slog.Logger
	mu     sync.Mutex
	// busy holds the stacks being changed now; entries go when the change ends, so it is
	// bounded by the requests in flight.
	busy map[string]bool
}

// NewControl returns the agent's control service, which syncs, deploys and removes the
// stacks of runner:
//   - PUT /v1/stacks/{name} {"compose": "...", "env": "..."} writes the files (204)
//   - POST /v1/stacks/{name}/up {"pull": bool, "credentials": [{"address", "username",
//     "password"}]} and POST /v1/stacks/{name}/down {"volumes": bool} run compose (up with
//     the registry credentials, see writeDockerConfig) and stream JSON lines
//     {"stream", "line"}, then {"exit_code": N} or {"error": "..."}; cancelling the
//     request kills compose
//   - DELETE /v1/stacks/{name} removes the files (204)
//
// Errors are JSON {"error": "..."}: 400 bad request, 404 no such stack, 409 the stack is
// being changed, 503 no docker-compose.
func NewControl(runner Runner, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &control{runner: runner, log: log, busy: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/stacks/{name}", c.sync)
	mux.HandleFunc("POST /v1/stacks/{name}/up", c.up)
	mux.HandleFunc("POST /v1/stacks/{name}/down", c.down)
	mux.HandleFunc("DELETE /v1/stacks/{name}", c.remove)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		controlError(w, http.StatusNotFound, "not found")
	})
	return mux
}

func controlError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(controlEvent{Error: msg})
}

// begin validates the stack name and marks the stack busy; it answers the request itself
// when it returns false. end must follow a true.
func (c *control) begin(w http.ResponseWriter, r *http.Request, v any) (string, bool) {
	name := r.PathValue("name")
	if !ValidName(name) {
		controlError(w, http.StatusBadRequest, "invalid stack name")
		return "", false
	}
	if v != nil {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlBody))
		// Server and agent are built together; an unknown field is a version mismatch.
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
			controlError(w, http.StatusBadRequest, "invalid JSON body")
			return "", false
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy[name] {
		controlError(w, http.StatusConflict, ErrBusy.Error())
		return "", false
	}
	c.busy[name] = true
	return name, true
}

func (c *control) end(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.busy, name)
}

func (c *control) sync(w http.ResponseWriter, r *http.Request) {
	var f controlFiles
	name, ok := c.begin(w, r, &f)
	if !ok {
		return
	}
	defer c.end(name)
	if err := c.runner.Sync(r.Context(), name, Files(f)); err != nil {
		c.log.Error("control: sync stack", "stack", name, "err", err)
		controlError(w, http.StatusInternalServerError, "the stack's files could not be written")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *control) remove(w http.ResponseWriter, r *http.Request) {
	name, ok := c.begin(w, r, nil)
	if !ok {
		return
	}
	defer c.end(name)
	err := c.runner.Remove(r.Context(), name)
	switch {
	case errors.Is(err, ErrNotFound):
		controlError(w, http.StatusNotFound, ErrNotFound.Error())
	case err != nil:
		c.log.Error("control: remove stack", "stack", name, "err", err)
		controlError(w, http.StatusInternalServerError, "the stack's files could not be removed")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// upRequest is the body of POST /v1/stacks/{name}/up. Credentials are left out when
// there are none, so an agent built before them still deploys public images.
type upRequest struct {
	Pull        bool         `json:"pull"`
	Credentials []Credential `json:"credentials,omitempty"`
}

func (c *control) up(w http.ResponseWriter, r *http.Request) {
	var req upRequest
	name, ok := c.begin(w, r, &req)
	if !ok {
		return
	}
	defer c.end(name)
	r = r.WithContext(withCredentials(r.Context(), req.Credentials))
	c.stream(w, r, "up", name, func(ctx context.Context, out func(Line)) (int, error) {
		return c.runner.Up(ctx, name, req.Pull, out)
	})
}

func (c *control) down(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Volumes bool `json:"volumes"`
	}
	name, ok := c.begin(w, r, &req)
	if !ok {
		return
	}
	defer c.end(name)
	c.stream(w, r, "down", name, func(ctx context.Context, out func(Line)) (int, error) {
		return c.runner.Down(ctx, name, req.Volumes, out)
	})
}

// stream runs compose and writes its output as JSON lines. The header goes out with the
// first line, so a compose that can't start gets an ordinary error.
func (c *control) stream(w http.ResponseWriter, r *http.Request, op, name string, run func(ctx context.Context, out func(Line)) (int, error)) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	started, alive := false, true
	send := func(ev controlEvent) {
		if !alive {
			return
		}
		if !started {
			started = true
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
		}
		alive = enc.Encode(ev) == nil && rc.Flush() == nil
		if !alive {
			cancel()
		}
	}
	code, err := run(ctx, func(l Line) { send(controlEvent{Stream: l.Stream, Line: l.Text}) })
	switch {
	case err == nil:
		send(controlEvent{ExitCode: &code})
	case !started && errors.Is(err, ErrNotFound):
		controlError(w, http.StatusNotFound, ErrNotFound.Error())
	case !started && errors.Is(err, ErrNoCompose):
		controlError(w, http.StatusServiceUnavailable, ErrNoCompose.Error())
	case ctx.Err() != nil:
		// The server went away; nobody reads an answer.
		c.log.Info("control: stack "+op+" cancelled", "stack", name)
	case !started:
		c.log.Error("control: stack "+op, "stack", name, "err", err)
		controlError(w, http.StatusInternalServerError, "compose could not run")
	default:
		c.log.Warn("control: stack "+op, "stack", name, "err", err)
		send(controlEvent{Error: "compose did not finish"})
	}
}

// DialFunc opens a connection to an agent's control service.
type DialFunc func(ctx context.Context) (net.Conn, error)

// Remote deploys stacks through an agent's control service.
type Remote struct {
	client *http.Client
	// dir has the server's copy of the stacks, which credentials picks from.
	dir         Dir
	credentials CredentialFunc
}

var _ Runner = (*Remote)(nil)

// NewRemote returns a runner that calls the control service over connections from dial,
// one per request.
func NewRemote(dial DialFunc) *Remote {
	return &Remote{client: &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		DisableKeepAlives: true,
	}}}
}

// Sync sends f to the agent.
func (rm *Remote) Sync(ctx context.Context, name string, f Files) error {
	resp, err := rm.do(ctx, http.MethodPut, name, "", controlFiles(f))
	if err != nil {
		return fmt.Errorf("sync stack %s: %w", name, err)
	}
	_ = resp.Body.Close()
	return nil
}

// Remove deletes the agent's copy of the stack.
func (rm *Remote) Remove(ctx context.Context, name string) error {
	resp, err := rm.do(ctx, http.MethodDelete, name, "", nil)
	if err != nil {
		return fmt.Errorf("remove stack %s: %w", name, err)
	}
	_ = resp.Body.Close()
	return nil
}

// WithCredentials makes rm send compose up the registry credentials creds finds for the
// server's copy of the stack in dir, and returns rm.
func (rm *Remote) WithCredentials(dir Dir, creds CredentialFunc) *Remote {
	rm.dir, rm.credentials = dir, creds
	return rm
}

// Up runs compose up on the agent, with the registry credentials it may need.
func (rm *Remote) Up(ctx context.Context, name string, pull bool, out func(Line)) (int, error) {
	req := upRequest{Pull: pull}
	if rm.credentials != nil {
		f, err := rm.dir.Read(name)
		if err != nil {
			return 0, fmt.Errorf("compose up %s: %w", name, err)
		}
		if req.Credentials, err = rm.credentials(ctx, f); err != nil {
			return 0, fmt.Errorf("compose up %s: registry credentials: %w", name, err)
		}
	}
	return rm.run(ctx, name, "up", req, out)
}

// Down runs compose down on the agent.
func (rm *Remote) Down(ctx context.Context, name string, volumes bool, out func(Line)) (int, error) {
	return rm.run(ctx, name, "down", map[string]bool{"volumes": volumes}, out)
}

func (rm *Remote) run(ctx context.Context, name, op string, body any, out func(Line)) (int, error) {
	resp, err := rm.do(ctx, http.MethodPost, name, "/"+op, body)
	if err != nil {
		return 0, fmt.Errorf("compose %s %s: %w", op, name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventSize)
	for sc.Scan() {
		var ev controlEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return 0, fmt.Errorf("compose %s %s: bad output from the agent: %w", op, name, err)
		}
		switch {
		case ev.ExitCode != nil:
			return *ev.ExitCode, nil
		case ev.Error != "":
			return 0, fmt.Errorf("compose %s %s: agent: %s", op, name, ev.Error)
		case ev.Stream == "stdout" || ev.Stream == "stderr":
			out(Line{Stream: ev.Stream, Text: ev.Line})
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("compose %s %s: %w", op, name, err)
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("compose %s %s: %w", op, name, err)
	}
	return 0, fmt.Errorf("compose %s %s: the agent's output ended without an exit code", op, name)
}

// do sends a control request and returns the response if it succeeded; an error status
// comes back as ErrNotFound, ErrBusy, ErrNoCompose or an error with the agent's message.
func (rm *Remote) do(ctx context.Context, method, name, suffix string, body any) (*http.Response, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("stack name %q is invalid", name)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent/v1/stacks/"+name+suffix, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := rm.client.Do(req)
	if err != nil {
		// Drop the url.Error, whose message repeats the made-up URL.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return nil, uerr.Err
		}
		return nil, err
	}
	if resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var ev controlEvent
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&ev)
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusConflict:
		return nil, ErrBusy
	case http.StatusServiceUnavailable:
		return nil, ErrNoCompose
	}
	// The text ends up in the server's log: printable and short.
	msg := strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(ev.Error))
	if len(msg) > 256 {
		msg = strings.ToValidUTF8(msg[:256], "")
	}
	if msg == "" {
		msg = resp.Status
	}
	return nil, fmt.Errorf("agent answered %d: %s", resp.StatusCode, msg)
}
