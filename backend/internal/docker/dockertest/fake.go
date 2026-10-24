// Package dockertest has a fake docker.Client for tests, so they never need a daemon.
package dockertest

import (
	"context"
	"sync"

	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

var _ docker.Client = (*Fake)(nil)

// Fake answers with its fields; set an Err to make that call fail. It is safe for
// concurrent use; set the fields before sharing it.
type Fake struct {
	PingErr       error
	InfoResult    client.SystemInfoResult
	InfoErr       error
	VersionResult client.ServerVersionResult
	VersionErr    error
	Resources

	mu     sync.Mutex
	pings  int
	closed bool
}

// Ping counts the call and answers with PingErr.
func (f *Fake) Ping(ctx context.Context, _ client.PingOptions) (client.PingResult, error) {
	f.mu.Lock()
	f.pings++
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return client.PingResult{}, err
	}
	return client.PingResult{APIVersion: "1.51"}, f.PingErr
}

// Info answers with InfoResult and InfoErr.
func (f *Fake) Info(ctx context.Context, _ client.InfoOptions) (client.SystemInfoResult, error) {
	if err := ctx.Err(); err != nil {
		return client.SystemInfoResult{}, err
	}
	return f.InfoResult, f.InfoErr
}

// ServerVersion answers with VersionResult and VersionErr.
func (f *Fake) ServerVersion(ctx context.Context, _ client.ServerVersionOptions) (client.ServerVersionResult, error) {
	if err := ctx.Err(); err != nil {
		return client.ServerVersionResult{}, err
	}
	return f.VersionResult, f.VersionErr
}

// Close records the call.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// Pings returns how often Ping was called.
func (f *Fake) Pings() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pings
}

// Closed reports whether Close was called.
func (f *Fake) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}
