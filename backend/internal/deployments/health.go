package deployments

import (
	"fmt"
	"regexp"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
)

// Bounds of the health settings.
const (
	MaxWaitSeconds   = 30 * 60
	MaxStableSeconds = 10 * 60
	// defaultCheckTimeout is an HTTP check's timeout when none is set.
	defaultCheckTimeout = 5
)

// servicePattern is a compose service name.
var servicePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// Health says when a run counts as succeeded. The zero value checks nothing beyond
// compose's exit code.
type Health struct {
	// WaitSeconds, if set, has compose wait this long for the services to be running
	// and healthy (`up --wait`).
	WaitSeconds int `json:"wait_seconds"`
	// StableSeconds, if set, is how long every service's containers must then keep
	// running without a restart.
	StableSeconds int `json:"stable_seconds"`
	// HTTP, if set, is a request a service must answer as expected.
	HTTP *HTTPCheck `json:"http,omitempty"`
	// Rollback redeploys the replaced variable values when a check fails.
	Rollback bool `json:"rollback"`
}

// HTTPCheck is a GET of http://127.0.0.1:Port/Path inside a container of Service.
type HTTPCheck struct {
	Service        string `json:"service"`
	Port           int    `json:"port"`
	Path           string `json:"path"`
	Status         int    `json:"status"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Wait is WaitSeconds as a duration.
func (h Health) Wait() time.Duration { return time.Duration(h.WaitSeconds) * time.Second }

// Stable is StableSeconds as a duration.
func (h Health) Stable() time.Duration { return time.Duration(h.StableSeconds) * time.Second }

// Request is the check of c in container id.
func (c HTTPCheck) Request(id string) probe.Request {
	return probe.Request{Container: id, Port: c.Port, Path: c.Path, Status: c.Status, Timeout: time.Duration(c.TimeoutSeconds) * time.Second}
}

// clean fills in the defaults and checks the bounds.
func (h *Health) clean() error {
	if h.WaitSeconds < 0 || h.WaitSeconds > MaxWaitSeconds {
		return &InputError{Msg: fmt.Sprintf("wait_seconds must be 0 to %d", MaxWaitSeconds)}
	}
	if h.StableSeconds < 0 || h.StableSeconds > MaxStableSeconds {
		return &InputError{Msg: fmt.Sprintf("stable_seconds must be 0 to %d", MaxStableSeconds)}
	}
	c := h.HTTP
	if c == nil {
		return nil
	}
	if c.Path == "" {
		c.Path = "/"
	}
	if c.Status == 0 {
		c.Status = 200
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = defaultCheckTimeout
	}
	switch {
	case !servicePattern.MatchString(c.Service):
		return &InputError{Msg: "http.service must name a compose service"}
	case c.Port < 1 || c.Port > 65535:
		return &InputError{Msg: "http.port must be 1 to 65535"}
	case !probe.ValidPath(c.Path):
		return &InputError{Msg: "http.path must start with / and hold only URL characters"}
	case c.Status < 100 || c.Status > 599:
		return &InputError{Msg: "http.status must be 100 to 599"}
	case c.TimeoutSeconds < 1 || time.Duration(c.TimeoutSeconds)*time.Second > probe.MaxTimeout:
		return &InputError{Msg: fmt.Sprintf("http.timeout_seconds must be 1 to %d", int(probe.MaxTimeout/time.Second))}
	}
	return nil
}
