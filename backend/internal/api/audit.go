package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/audit"
)

// unaudited are writes the audit log leaves out: logins and logouts change no state
// worth recording, and failed logins are logged and rate limited already.
var unaudited = map[string]bool{
	"POST /api/v1/auth/login":  true,
	"POST /api/v1/auth/logout": true,
}

// auditTimeout bounds recording an entry after the response.
const auditTimeout = 5 * time.Second

type auditKey struct{}

// auditNote is what a handler adds to its request's audit entry.
type auditNote struct {
	environment string
	target      string
	// status replaces the answered one for a stream that failed after its 200.
	status int
	// recordNow records the entry at once; recorded says it was.
	recordNow func(r *http.Request, status int)
	recorded  bool
}

// auditTarget names the object a write acts on when the path doesn't (a name from the
// body); without it the entry's target is the path's {id}.
func auditTarget(r *http.Request, target string) {
	if n, ok := r.Context().Value(auditKey{}).(*auditNote); ok {
		n.target = target
	}
}

// auditEnvironment names the environment of a write whose path has no {env}.
func auditEnvironment(r *http.Request, env string) {
	if n, ok := r.Context().Value(auditKey{}).(*auditNote); ok {
		n.environment = env
	}
}

// auditStatus records status instead of the one answered: a stream that already sent
// 200 and then failed.
func auditStatus(r *http.Request, status int) {
	if n, ok := r.Context().Value(auditKey{}).(*auditNote); ok {
		n.status = status
	}
}

// auditNow records a long-lived read, an exec session, when it starts instead of
// waiting for it to end, with the status so far. Later calls do nothing.
func auditNow(r *http.Request, status int) {
	if n, ok := r.Context().Value(auditKey{}).(*auditNote); ok && n.recordNow != nil && !n.recorded {
		n.recorded = true
		n.recordNow(r, status)
	}
}

// statusRecorder remembers the status a handler answered.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection (flushing a pull stream).
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// serveAudited serves r and records it in the audit log when it is a write, or a read
// whose handler calls auditNow. This is the only place entries are recorded.
// Unauthenticated writes (setup, agent enrollment) are recorded only when they succeed,
// so anyone who can reach nudge can't fill the log.
func (s *server) serveAudited(api *http.ServeMux, w http.ResponseWriter, r *http.Request) {
	if s.audit == nil {
		api.ServeHTTP(w, r)
		return
	}
	// A long write (a pull) is stamped with its start.
	start := time.Now()
	note := &auditNote{}
	note.recordNow = func(r *http.Request, status int) { s.record(r, start, note, status) }
	r = r.WithContext(context.WithValue(r.Context(), auditKey{}, note))
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		api.ServeHTTP(w, r)
		return
	}
	rec := &statusRecorder{ResponseWriter: w}
	api.ServeHTTP(rec, r)
	if note.recorded {
		return
	}
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	s.record(r, start, note, status)
}

// record writes r's entry: the route the mux matched, who asked and the status, unless
// the note replaces it.
func (s *server) record(r *http.Request, start time.Time, note *auditNote, status int) {
	// The mux set r.Pattern; the catch-all and unmatched methods name no action.
	if r.Pattern == "" || r.Pattern == "/api/" || unaudited[r.Pattern] {
		return
	}
	if note.status != 0 {
		status = note.status
	}
	u, authenticated := userFrom(r.Context())
	if !authenticated && status >= 400 {
		return
	}
	e := audit.Entry{
		Time: start, Username: u.Username, Token: u.Token, Environment: r.PathValue("env"),
		Action: r.Pattern, Target: r.PathValue("id"), Status: status,
	}
	if note.environment != "" {
		e.Environment = note.environment
	}
	if note.target != "" {
		e.Target = note.target
	}
	// The client may be gone (a cancelled pull); the write happened all the same.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	if err := s.audit.Record(ctx, e); err != nil {
		s.log.Error("audit", "err", err, "action", e.Action)
	}
}

type auditEntryResponse struct {
	ID          int64     `json:"id"`
	Time        time.Time `json:"time"`
	User        string    `json:"user"`
	Token       string    `json:"token"`
	Environment string    `json:"environment"`
	Action      string    `json:"action"`
	Target      string    `json:"target"`
	Status      int       `json:"status"`
}

type auditListResponse struct {
	Entries []auditEntryResponse `json:"entries"`
	// Next is the before= value of the next page, nil on the last one.
	Next *int64 `json:"next"`
}

func (s *server) listAudit(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	q := audit.Query{
		Username: params.Get("user"), Environment: params.Get("environment"), Search: params.Get("q"),
	}
	var ok bool
	if q.Before, ok = s.queryInt(w, r, "before"); !ok {
		return
	}
	limit, ok := s.queryInt(w, r, "limit")
	if !ok {
		return
	}
	if limit > audit.MaxLimit {
		s.writeError(w, http.StatusBadRequest, "limit must be at most "+strconv.Itoa(audit.MaxLimit))
		return
	}
	q.Limit = int(limit)
	if q.Limit == 0 {
		q.Limit = audit.DefaultLimit
	}
	if params.Get("failed") != "" {
		failed, ok := s.queryBool(w, r, "failed")
		if !ok {
			return
		}
		q.Failed = &failed
	}
	if q.Since, ok = s.queryTime(w, r, "since"); !ok {
		return
	}
	if q.Until, ok = s.queryTime(w, r, "until"); !ok {
		return
	}
	entries, more, err := s.audit.List(r.Context(), q)
	if err != nil {
		s.internalError(w, "list audit log", err)
		return
	}
	var next *int64
	if more {
		next = &entries[len(entries)-1].ID
	}
	resp := auditListResponse{Entries: make([]auditEntryResponse, 0, len(entries)), Next: next}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, auditEntryResponse{
			ID: e.ID, Time: e.Time.UTC(), User: e.Username, Token: e.Token, Environment: e.Environment,
			Action: e.Action, Target: e.Target, Status: e.Status,
		})
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// queryInt parses a non-negative integer query parameter, 0 when absent; it answers 400
// itself when the value is invalid.
func (s *server) queryInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		s.writeError(w, http.StatusBadRequest, name+" must be a non-negative integer")
		return 0, false
	}
	return n, true
}

// queryTime parses an RFC 3339 query parameter, zero when absent; it answers 400 itself
// when the value is invalid.
func (s *server) queryTime(w http.ResponseWriter, r *http.Request, name string) (time.Time, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, name+" must be an RFC 3339 time")
		return time.Time{}, false
	}
	return t, true
}
