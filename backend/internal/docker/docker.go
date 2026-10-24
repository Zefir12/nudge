// Package docker builds Docker API clients for environments. Every kind of environment
// gets the same client; only the dialer differs (the local socket now, an agent's tunnel
// later), so each Docker feature works the same everywhere.
package docker

import (
	"context"
	"errors"
	"fmt"
	"net"

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
