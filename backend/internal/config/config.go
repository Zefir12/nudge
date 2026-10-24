// Package config loads nudge's settings from flags, with NUDGE_* environment variables
// as fallbacks and defaults that work in the published image without any setup.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
)

// Server is the configuration of `nudge serve`.
type Server struct {
	// Addr is the listen address of the API and web UI.
	Addr string
	// DataDir holds the database and stack files. The image mounts a volume here.
	DataDir string
	// LogLevel is debug, info, warn or error.
	LogLevel slog.Level
	// AdminPassword, if set, creates the user "admin" at start when no user exists.
	// Environment only (NUDGE_ADMIN_PASSWORD): a flag would show in the process list.
	AdminPassword string
	// DockerHost is the local environment's Docker API, unix:// or tcp://.
	DockerHost string
}

// LoadServer parses args (without the program and subcommand name). A flag wins over
// its environment variable, which wins over the default. Usage and errors go to out.
func LoadServer(args []string, getenv func(string) string, out io.Writer) (Server, error) {
	var c Server
	fs := flag.NewFlagSet("nudge serve", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&c.Addr, "addr", envOr(getenv, "NUDGE_ADDR", ":8080"), "listen address (NUDGE_ADDR)")
	fs.StringVar(&c.DataDir, "data", envOr(getenv, "NUDGE_DATA", "/data"), "data directory (NUDGE_DATA)")
	fs.StringVar(&c.DockerHost, "docker-host", envOr(getenv, "DOCKER_HOST", "unix:///var/run/docker.sock"), "Docker API of the local environment, unix:// or tcp:// (DOCKER_HOST)")
	level := fs.String("log-level", envOr(getenv, "NUDGE_LOG_LEVEL", "info"), "debug, info, warn or error (NUDGE_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return Server{}, err
	}
	if fs.NArg() > 0 {
		return Server{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := c.LogLevel.UnmarshalText([]byte(*level)); err != nil {
		return Server{}, fmt.Errorf("log level %q: %w", *level, err)
	}
	c.AdminPassword = getenv("NUDGE_ADMIN_PASSWORD")
	if c.Addr == "" {
		return Server{}, errors.New("listen address must not be empty")
	}
	if c.DockerHost == "" {
		return Server{}, errors.New("docker host must not be empty")
	}
	if c.DataDir == "" {
		return Server{}, errors.New("data directory must not be empty")
	}
	return c, nil
}

// Agent is the configuration of `nudge agent`.
type Agent struct {
	// ServerURL is the server's public URL, https:// (http:// only with Insecure).
	ServerURL *url.URL
	// JoinToken enrolls the agent on its first connect; afterwards agent.json is used.
	JoinToken string
	// DataDir holds agent.json (agent ID and secret).
	DataDir string
	// Insecure allows an http:// server URL.
	Insecure bool
	// LogLevel is debug, info, warn or error.
	LogLevel slog.Level
	// DockerHost is the Docker API streams are piped to, unix:// or tcp://.
	DockerHost string
}

// LoadAgent parses the agent's args like LoadServer.
func LoadAgent(args []string, getenv func(string) string, out io.Writer) (Agent, error) {
	var c Agent
	fs := flag.NewFlagSet("nudge agent", flag.ContinueOnError)
	fs.SetOutput(out)
	server := fs.String("server", getenv("NUDGE_SERVER_URL"), "the server's public URL (NUDGE_SERVER_URL)")
	// No env default here: -h prints defaults, and the token is a secret.
	fs.StringVar(&c.JoinToken, "join-token", "", "one-time token from \"Add environment\", needed only on the first connect (NUDGE_JOIN_TOKEN)")
	fs.StringVar(&c.DataDir, "data", envOr(getenv, "NUDGE_DATA", "/data"), "data directory for agent.json (NUDGE_DATA)")
	insecure, err := strconv.ParseBool(envOr(getenv, "NUDGE_INSECURE", "false"))
	if err != nil {
		return Agent{}, fmt.Errorf("NUDGE_INSECURE %q: want true or false", getenv("NUDGE_INSECURE"))
	}
	fs.BoolVar(&c.Insecure, "insecure", insecure, "allow an http:// server URL, for testing only (NUDGE_INSECURE)")
	fs.StringVar(&c.DockerHost, "docker-host", envOr(getenv, "DOCKER_HOST", "unix:///var/run/docker.sock"), "Docker API to serve, unix:// or tcp:// (DOCKER_HOST)")
	level := fs.String("log-level", envOr(getenv, "NUDGE_LOG_LEVEL", "info"), "debug, info, warn or error (NUDGE_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return Agent{}, err
	}
	if fs.NArg() > 0 {
		return Agent{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if c.JoinToken == "" {
		c.JoinToken = getenv("NUDGE_JOIN_TOKEN")
	}
	if err := c.LogLevel.UnmarshalText([]byte(*level)); err != nil {
		return Agent{}, fmt.Errorf("log level %q: %w", *level, err)
	}
	if c.ServerURL, err = parseServerURL(*server, c.Insecure); err != nil {
		return Agent{}, err
	}
	if c.DockerHost == "" {
		return Agent{}, errors.New("docker host must not be empty")
	}
	if c.DataDir == "" {
		return Agent{}, errors.New("data directory must not be empty")
	}
	return c, nil
}

func parseServerURL(s string, insecure bool) (*url.URL, error) {
	if s == "" {
		return nil, errors.New("server URL is required (-server or NUDGE_SERVER_URL)")
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("server URL %q: %w", s, err)
	}
	switch {
	case u.Scheme == "http" && !insecure:
		return nil, fmt.Errorf("server URL %q: http:// sends the agent secret in clear text; use https:// or set -insecure for testing", s)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("server URL %q: want https://", s)
	case u.Host == "":
		return nil, fmt.Errorf("server URL %q: missing host", s)
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("server URL %q: want only scheme, host and an optional path", s)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
