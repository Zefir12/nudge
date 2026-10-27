package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
)

// deployIdle is how long `nudge deploy` waits for a line of the progress stream; the
// server sends one at least every 25 s.
const deployIdle = 2 * time.Minute

// runProbe is `nudge probe [-status N] [-timeout D] URL`, the HTTP check a deployment
// runs in a helper container: one GET, exit 0 when the answer has the status.
func runProbe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nudge probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	status := fs.Int("status", 200, "expected HTTP status")
	timeout := fs.Duration("timeout", 5*time.Second, "how long to wait for the answer")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return probe.ExitBadInput
	}
	if fs.NArg() != 1 || *timeout <= 0 || *timeout > probe.MaxTimeout {
		_, _ = fmt.Fprintln(stderr, "usage: nudge probe [-status N] [-timeout D] URL")
		return probe.ExitBadInput
	}
	code, msg := probe.Check(ctx, fs.Arg(0), *status, *timeout)
	_, _ = fmt.Fprintln(stdout, msg)
	return code
}

// varsFlag collects repeated --var NAME=value.
type varsFlag map[string]string

func (v varsFlag) String() string { return "" }

func (v varsFlag) Set(s string) error {
	name, value, ok := strings.Cut(s, "=")
	if !ok || name == "" {
		return errors.New("want NAME=value")
	}
	v[name] = value
	return nil
}

// deployEvent is a line of the server's progress stream (api.deployEvent).
type deployEvent struct {
	Step     string   `json:"step"`
	Service  string   `json:"service"`
	Message  string   `json:"message"`
	Warning  bool     `json:"warning"`
	Status   string   `json:"status"`
	Run      int64    `json:"run"`
	Error    string   `json:"error"`
	Warnings []string `json:"warnings"`
}

// runDeploy is `nudge deploy`: it starts a run of a deployment with the deploy token in
// NUDGE_DEPLOY_TOKEN, prints its progress and exits 0 only if it succeeded.
func runDeploy(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nudge deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", getenv("NUDGE_SERVER_URL"), "nudge server URL (env NUDGE_SERVER_URL)")
	name := fs.String("deployment", "", "deployment name")
	vars := varsFlag{}
	fs.Var(vars, "var", "variable to set, NAME=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	token := getenv("NUDGE_DEPLOY_TOKEN")
	base, err := url.Parse(*server)
	switch {
	case fs.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "":
		err = errors.New("--server must be an http(s) URL")
	case *name == "":
		err = errors.New("--deployment is required")
	case token == "":
		err = errors.New("NUDGE_DEPLOY_TOKEN is not set")
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nudge deploy:", err)
		return 2
	}
	body, err := json.Marshal(map[string]any{"variables": vars})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nudge deploy:", err)
		return 2
	}
	target := base.JoinPath("/api/v1/deployments", *name, "deploys")
	target.RawQuery = "stream=1"

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nudge deploy:", err)
		return 2
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	// The stream lasts as long as the run; only connecting and the first answer are bounded.
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: time.Minute,
	}}
	resp, err := client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nudge deploy:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		_, _ = fmt.Fprintf(stderr, "nudge deploy: %s (HTTP %d)\n", e.Error, resp.StatusCode)
		return 1
	}

	lines := make(chan []byte)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case lines <- append([]byte(nil), sc.Bytes()...):
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		var line []byte
		select {
		case l, ok := <-lines:
			if !ok {
				_, _ = fmt.Fprintln(stderr, "nudge deploy: the stream ended without a result; see the deployment's runs")
				return 1
			}
			line = l
		case <-time.After(deployIdle):
			_, _ = fmt.Fprintln(stderr, "nudge deploy: the server went quiet; see the deployment's runs")
			return 1
		case <-ctx.Done():
			_, _ = fmt.Fprintln(stderr, "nudge deploy: interrupted; the run goes on on the server")
			return 1
		}
		var ev deployEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if code, done := printDeployEvent(stdout, stderr, ev); done {
			return code
		}
	}
}

// printDeployEvent prints a progress line; on the final line it returns the exit code.
func printDeployEvent(stdout, stderr io.Writer, ev deployEvent) (code int, done bool) {
	switch {
	case ev.Status != "":
		_, _ = fmt.Fprintf(stdout, "run %d: %s\n", ev.Run, ev.Status)
		if ev.Error != "" {
			_, _ = fmt.Fprintln(stderr, ev.Error)
		}
		if ev.Status == "succeeded" {
			return 0, true
		}
		return 1, true
	case ev.Error != "":
		_, _ = fmt.Fprintln(stderr, "nudge deploy:", ev.Error)
		return 1, true
	case ev.Message == "" && ev.Step == "":
		// A keepalive.
		return 0, false
	}
	prefix := ""
	if ev.Step != "" {
		prefix = "[" + ev.Step + "] "
	}
	if ev.Service != "" {
		prefix += ev.Service + ": "
	}
	if ev.Warning {
		prefix += "warning: "
	}
	msg := ev.Message
	if msg == "" {
		msg = "…"
	}
	if ev.Run != 0 && ev.Step == "" {
		msg = fmt.Sprintf("run %d %s", ev.Run, msg)
	}
	_, _ = fmt.Fprintln(stdout, prefix+msg)
	return 0, false
}
