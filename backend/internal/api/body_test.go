package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// deadlineWriter records the read deadlines set through http.ResponseController.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
	sets     []time.Time
}

func (d *deadlineWriter) SetReadDeadline(t time.Time) error {
	d.deadline = t
	d.sets = append(d.sets, t)
	return nil
}

// deadlineBody fails a test when it is read without a read deadline in place.
type deadlineBody struct {
	t *testing.T
	w *deadlineWriter
	r io.Reader
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	if b.w.deadline.IsZero() {
		b.t.Error("body read without a read deadline")
	}
	return b.r.Read(p)
}

func (b *deadlineBody) Close() error { return nil }

func TestDecodersSetReadDeadline(t *testing.T) {
	t.Parallel()
	s := &server{log: slog.New(slog.DiscardHandler)}
	decoders := map[string]func(http.ResponseWriter, *http.Request, any) bool{
		"decodeJSON":   s.decodeJSON,
		"decodeStrict": s.decodeStrict,
	}
	tests := []struct {
		name   string
		body   string
		wantOK bool
	}{
		{name: "valid", body: `{"a":1}`, wantOK: true},
		{name: "malformed", body: `{"a":`, wantOK: false},
	}
	for dname, decode := range decoders {
		for _, tt := range tests {
			t.Run(dname+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
				r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
				r.Body = &deadlineBody{t: t, w: w, r: strings.NewReader(tt.body)}
				before := time.Now()
				var v struct {
					A int `json:"a"`
				}
				if got := decode(w, r, &v); got != tt.wantOK {
					t.Fatalf("decode = %v, want %v", got, tt.wantOK)
				}
				if len(w.sets) != 2 {
					t.Fatalf("deadline set %d times, want 2 (set, then cleared)", len(w.sets))
				}
				if d := w.sets[0].Sub(before); d < bodyReadTimeout || d > bodyReadTimeout+time.Minute {
					t.Errorf("deadline %v from now, want about %v", d, bodyReadTimeout)
				}
				if !w.sets[1].IsZero() {
					t.Errorf("deadline not cleared after the body: %v", w.sets[1])
				}
			})
		}
	}
}
