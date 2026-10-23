// Package config loads nudge's settings from flags, with NUDGE_* environment variables
// as fallbacks and defaults that work in the published image without any setup.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
}

// LoadServer parses args (without the program and subcommand name). A flag wins over
// its environment variable, which wins over the default. Usage and errors go to out.
func LoadServer(args []string, getenv func(string) string, out io.Writer) (Server, error) {
	var c Server
	fs := flag.NewFlagSet("nudge serve", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&c.Addr, "addr", envOr(getenv, "NUDGE_ADDR", ":8080"), "listen address (NUDGE_ADDR)")
	fs.StringVar(&c.DataDir, "data", envOr(getenv, "NUDGE_DATA", "/data"), "data directory (NUDGE_DATA)")
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
	if c.Addr == "" {
		return Server{}, errors.New("listen address must not be empty")
	}
	if c.DataDir == "" {
		return Server{}, errors.New("data directory must not be empty")
	}
	return c, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
