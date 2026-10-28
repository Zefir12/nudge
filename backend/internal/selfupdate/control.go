package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// ErrOldAgent means the agent doesn't answer GET /v1/self: it predates self-updates and
// has to be updated by hand once.
var ErrOldAgent = errors.New("this agent is too old to update itself: update it once on its host (docker compose pull && docker compose up -d)")

// selfJSON is an agent's answer to GET /v1/self.
type selfJSON struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	DockerHost string `json:"docker_host"`
}

// Control serves GET /v1/self on an agent's control service, so the server can find the
// agent's container; every other request goes to next.
func Control(self stacks.Self, dockerHost string, next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/self", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(selfJSON{ID: self.ID, Hostname: self.Hostname, DockerHost: dockerHost})
	})
	mux.Handle("/", next)
	return mux
}

// RemoteSelf asks an agent's control service, over a connection from dial, for its own
// container and Docker host.
func RemoteSelf(ctx context.Context, dial stacks.DialFunc) (stacks.Self, string, error) {
	hc := &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://agent/v1/self", nil)
	if err != nil {
		return stacks.Self{}, "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return stacks.Self{}, "", fmt.Errorf("ask the agent for its container: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return stacks.Self{}, "", ErrOldAgent
	}
	if resp.StatusCode != http.StatusOK {
		return stacks.Self{}, "", fmt.Errorf("ask the agent for its container: status %d", resp.StatusCode)
	}
	var s selfJSON
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&s); err != nil {
		return stacks.Self{}, "", fmt.Errorf("ask the agent for its container: %w", err)
	}
	return stacks.Self{ID: s.ID, Hostname: s.Hostname}, s.DockerHost, nil
}
