package dockertest

import (
	"context"
	"fmt"
	"net"
	"slices"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// Exec is an attached exec session as the container sees it: Conn is the process's TTY
// (read what the user typed, write its output, close it to end the process).
type Exec struct {
	ID          string
	ContainerID string
	Options     client.ExecCreateOptions
	Conn        net.Conn
}

// ExecCreate records the options and answers with the ID "exec-<n>"; a container
// missing from ContainerDetails is not found.
func (f *Fake) ExecCreate(ctx context.Context, id string, opts client.ExecCreateOptions) (client.ExecCreateResult, error) {
	if _, err := lookup(ctx, f, f.ContainerDetails, id); err != nil {
		return client.ExecCreateResult{}, err
	}
	if f.ExecErr != nil {
		return client.ExecCreateResult{}, f.ExecErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execs == nil {
		f.execs = map[string]Exec{}
	}
	e := Exec{ID: fmt.Sprintf("exec-%d", len(f.execs)+1), ContainerID: id, Options: opts}
	f.execs[e.ID] = e
	return client.ExecCreateResult{ID: e.ID}, nil
}

// ExecAttach connects the session over a pipe and hands the container's end to Execs.
func (f *Fake) ExecAttach(ctx context.Context, execID string, _ client.ExecAttachOptions) (client.ExecAttachResult, error) {
	f.mu.Lock()
	e, ok := f.execs[execID]
	f.mu.Unlock()
	if !ok {
		return client.ExecAttachResult{}, cerrdefs.ErrNotFound
	}
	ours, theirs := net.Pipe()
	e.Conn = theirs
	if f.Execs == nil {
		_ = theirs.Close()
	} else {
		select {
		case f.Execs <- &e:
		case <-ctx.Done():
			_, _ = ours.Close(), theirs.Close()
			return client.ExecAttachResult{}, ctx.Err()
		}
	}
	return client.ExecAttachResult{HijackedResponse: client.NewHijackedResponse(ours, "application/vnd.docker.raw-stream")}, nil
}

// ExecResize records the size.
func (f *Fake) ExecResize(_ context.Context, execID string, opts client.ExecResizeOptions) (client.ExecResizeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.execs[execID]; !ok {
		return client.ExecResizeResult{}, cerrdefs.ErrNotFound
	}
	f.resizes = append(f.resizes, opts)
	return client.ExecResizeResult{}, nil
}

// ExecInspect answers that the session ended with ExecExitCode.
func (f *Fake) ExecInspect(_ context.Context, execID string, _ client.ExecInspectOptions) (client.ExecInspectResult, error) {
	f.mu.Lock()
	e, ok := f.execs[execID]
	f.mu.Unlock()
	if !ok {
		return client.ExecInspectResult{}, cerrdefs.ErrNotFound
	}
	return client.ExecInspectResult{ID: e.ID, ContainerID: e.ContainerID, ExitCode: f.ExecExitCode}, nil
}

// ExecResizes returns every ExecResize size so far.
func (f *Fake) ExecResizes() []client.ExecResizeOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resizes)
}
