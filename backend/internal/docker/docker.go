// Package docker builds Docker API clients for environments. Every kind of environment
// gets the same client; only the dialer differs (the local socket now, an agent's tunnel
// later), so each Docker feature works the same everywhere.
package docker

import (
	"context"
	"errors"
	"fmt"
	"net"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// DefaultHost is the local Docker socket used when DOCKER_HOST is unset.
const DefaultHost = "unix:///var/run/docker.sock"

// Client is the part of the Docker API that nudge's handlers use; *client.Client
// implements it and tests use fakes. Add methods here as handlers need them.
type Client interface {
	Ping(ctx context.Context, opts client.PingOptions) (client.PingResult, error)
	Info(ctx context.Context, opts client.InfoOptions) (client.SystemInfoResult, error)
	ServerVersion(ctx context.Context, opts client.ServerVersionOptions) (client.ServerVersionResult, error)
	ContainerList(ctx context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerRename(ctx context.Context, id string, opts client.ContainerRenameOptions) (client.ContainerRenameResult, error)
	ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(ctx context.Context, id string, opts client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRestart(ctx context.Context, id string, opts client.ContainerRestartOptions) (client.ContainerRestartResult, error)
	ContainerKill(ctx context.Context, id string, opts client.ContainerKillOptions) (client.ContainerKillResult, error)
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerLogs(ctx context.Context, id string, opts client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	ContainerStats(ctx context.Context, id string, opts client.ContainerStatsOptions) (client.ContainerStatsResult, error)
	ExecCreate(ctx context.Context, id string, opts client.ExecCreateOptions) (client.ExecCreateResult, error)
	ExecAttach(ctx context.Context, execID string, opts client.ExecAttachOptions) (client.ExecAttachResult, error)
	ExecResize(ctx context.Context, execID string, opts client.ExecResizeOptions) (client.ExecResizeResult, error)
	ExecInspect(ctx context.Context, execID string, opts client.ExecInspectOptions) (client.ExecInspectResult, error)
	ImageList(ctx context.Context, opts client.ImageListOptions) (client.ImageListResult, error)
	ImageInspect(ctx context.Context, id string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImagePull(ctx context.Context, ref string, opts client.ImagePullOptions) (client.ImagePullResponse, error)
	ImageRemove(ctx context.Context, id string, opts client.ImageRemoveOptions) (client.ImageRemoveResult, error)
	ImagePrune(ctx context.Context, opts client.ImagePruneOptions) (client.ImagePruneResult, error)
	DistributionInspect(ctx context.Context, ref string, opts client.DistributionInspectOptions) (client.DistributionInspectResult, error)
	VolumeList(ctx context.Context, opts client.VolumeListOptions) (client.VolumeListResult, error)
	VolumeInspect(ctx context.Context, id string, opts client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeCreate(ctx context.Context, opts client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeRemove(ctx context.Context, id string, opts client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
	VolumePrune(ctx context.Context, opts client.VolumePruneOptions) (client.VolumePruneResult, error)
	DiskUsage(ctx context.Context, opts client.DiskUsageOptions) (client.DiskUsageResult, error)
	NetworkList(ctx context.Context, opts client.NetworkListOptions) (client.NetworkListResult, error)
	NetworkInspect(ctx context.Context, id string, opts client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	NetworkCreate(ctx context.Context, name string, opts client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkRemove(ctx context.Context, id string, opts client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
	NetworkPrune(ctx context.Context, opts client.NetworkPruneOptions) (client.NetworkPruneResult, error)
	Events(ctx context.Context, opts client.EventsListOptions) client.EventsResult
	Close() error
}

// DialFunc opens a connection to a Docker API.
type DialFunc func(ctx context.Context) (net.Conn, error)

// LocalDialer dials a Docker host given as unix:///path or tcp://host:port.
func LocalDialer(host string) (DialFunc, error) {
	u, err := client.ParseHostURL(host)
	if err != nil {
		return nil, fmt.Errorf("docker host %q: %w", host, err)
	}
	var network, addr string
	switch u.Scheme {
	case "unix":
		network, addr = "unix", u.Host
	case "tcp":
		network, addr = "tcp", u.Host
	default:
		return nil, fmt.Errorf("docker host %q: unsupported scheme %q (want unix or tcp)", host, u.Scheme)
	}
	if addr == "" {
		return nil, fmt.Errorf("docker host %q: missing address", host)
	}
	var d net.Dialer
	return func(ctx context.Context) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}, nil
}

// New returns a client that reaches its Docker API through dial. The API version is
// negotiated on the first request.
func New(dial DialFunc) (*client.Client, error) {
	c, err := client.New(
		// The host only shapes the request URLs; every connection comes from dial.
		client.WithHost(DefaultHost),
		client.WithDialContext(func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return c, nil
}

// Unreachable reports whether err means the Docker API could not be reached at all, as
// opposed to the daemon answering with an error.
func Unreachable(err error) bool {
	var opErr *net.OpError
	return client.IsErrConnectionFailed(err) || errors.As(err, &opErr)
}

// NotFound reports whether the daemon answered that the object does not exist.
func NotFound(err error) bool {
	return cerrdefs.IsNotFound(err)
}

// Conflict reports whether the daemon refused a change because of the object's state,
// such as removing a running container without force.
func Conflict(err error) bool {
	return cerrdefs.IsConflict(err)
}

// ErrConflict is a conflict error for callers that turn another refusal into one.
var ErrConflict = cerrdefs.ErrConflict

// Forbidden reports whether the daemon refused a change it never allows in the object's
// current state, such as removing a predefined network or one with containers attached.
func Forbidden(err error) bool {
	return cerrdefs.IsPermissionDenied(err)
}

// InvalidArgument reports whether the daemon refused the request's parameters.
func InvalidArgument(err error) bool {
	return cerrdefs.IsInvalidArgument(err)
}

// Unauthorized reports whether the daemon (or a registry behind it) refused the
// credentials, or the lack of them.
func Unauthorized(err error) bool {
	return cerrdefs.IsUnauthorized(err) || cerrdefs.IsPermissionDenied(err)
}

// ErrNotFound is a not-found error for callers that hide an object from someone.
var ErrNotFound = cerrdefs.ErrNotFound
