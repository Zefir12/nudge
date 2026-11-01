// Package hostdisk tells how big the disk under Docker's root dir is and how much of it
// is free: statfs on the root dir mounted into nudge's container (read-only), answered
// for an agent's host by its control service (GET /v1/disk).
package hostdisk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// ErrUnknown means the disk's size can't be told: no root dir mounted, an agent too old
// to answer, or a platform without statfs.
var ErrUnknown = errors.New("disk size unknown")

// Space is a disk's size and what is free of it, in bytes.
type Space struct {
	Total uint64 `json:"total"`
	// Free is what unprivileged writers can still use (statfs's f_bavail).
	Free uint64 `json:"free"`
}

// Stat is the space of the filesystem root is on; ErrUnknown when root is empty or can't
// be read (not mounted).
func Stat(root string) (Space, error) {
	if root == "" {
		return Space{}, ErrUnknown
	}
	sp, err := statfs(root)
	if err != nil {
		return Space{}, fmt.Errorf("%w: %w", ErrUnknown, err)
	}
	return sp, nil
}

// Control serves GET /v1/disk on an agent's control service, the space under root (503
// when it's unknown); every other request goes to next.
func Control(root string, next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/disk", func(w http.ResponseWriter, _ *http.Request) {
		sp, err := Stat(root)
		if err != nil {
			http.Error(w, "disk size unknown", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sp)
	})
	mux.Handle("/", next)
	return mux
}

// Remote asks an agent's control service, over a connection from dial, for the space
// under its Docker root; ErrUnknown when the agent can't tell (no mount) or doesn't
// know the request (an older agent).
func Remote(ctx context.Context, dial func(context.Context) (net.Conn, error)) (Space, error) {
	hc := &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://agent/v1/disk", nil)
	if err != nil {
		return Space{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return Space{}, fmt.Errorf("ask the agent for its disk: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusServiceUnavailable:
		return Space{}, ErrUnknown
	default:
		return Space{}, fmt.Errorf("ask the agent for its disk: status %d", resp.StatusCode)
	}
	var sp Space
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&sp); err != nil {
		return Space{}, fmt.Errorf("ask the agent for its disk: %w", err)
	}
	return sp, nil
}
