package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunDeploy(t *testing.T) {
	t.Parallel()
	succeeded := `{"run":7,"message":"queued"}
{"step":"pulling"}
{}
{"step":"checking","service":"api","message":"the agent is too old for HTTP checks; the check was skipped","warning":true}
{"status":"succeeded","run":7,"step":"checking","warnings":["x"]}
`
	failed := `{"run":8,"message":"queued"}
{"status":"rolled_back","run":8,"error":"checking: service api: HTTP check: answered another status than 200; rolled back"}
`
	tests := []struct {
		name    string
		args    []string
		token   string
		status  int
		body    string
		code    int
		stdout  string
		stderr  string
		reached bool
	}{
		{name: "succeeded", args: []string{"--deployment", "shop", "--var", "API_IMAGE=app:v2"}, token: "nudge_dt_x", status: 200, body: succeeded,
			code: 0, stdout: "[checking] api: warning: the agent is too old", reached: true},
		{name: "rolled back fails the step", args: []string{"--deployment", "shop"}, token: "nudge_dt_x", status: 200, body: failed,
			code: 1, stdout: "run 8: rolled_back", stderr: "answered another status", reached: true},
		{name: "refused", args: []string{"--deployment", "shop"}, token: "nudge_dt_x", status: 401, body: `{"error":"invalid token"}`,
			code: 1, stderr: "invalid token (HTTP 401)", reached: true},
		{name: "stream cut", args: []string{"--deployment", "shop"}, token: "nudge_dt_x", status: 200, body: `{"step":"pulling"}` + "\n",
			code: 1, stderr: "without a result", reached: true},
		{name: "no token", args: []string{"--deployment", "shop"}, code: 2, stderr: "NUDGE_DEPLOY_TOKEN"},
		{name: "no deployment", token: "t", code: 2, stderr: "--deployment"},
		{name: "bad var", args: []string{"--deployment", "shop", "--var", "nope"}, token: "t", code: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var reached atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached.Store(true)
				var body struct {
					Variables map[string]string `json:"variables"`
				}
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &body)
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/deployments/shop/deploys" || r.URL.RawQuery != "stream=1" ||
					r.Header.Get("Authorization") != "Bearer "+tt.token || body.Variables == nil {
					t.Errorf("request = %s %s %q %s", r.Method, r.URL, r.Header.Get("Authorization"), b)
				}
				if tt.name == "succeeded" && body.Variables["API_IMAGE"] != "app:v2" {
					t.Errorf("variables = %v", body.Variables)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			getenv := func(k string) string {
				switch k {
				case "NUDGE_DEPLOY_TOKEN":
					return tt.token
				case "NUDGE_SERVER_URL":
					return srv.URL
				}
				return ""
			}
			var stdout, stderr bytes.Buffer
			code := realMain(context.Background(), append([]string{"deploy"}, tt.args...), getenv, &stdout, &stderr)
			if code != tt.code || !strings.Contains(stdout.String(), tt.stdout) || !strings.Contains(stderr.String(), tt.stderr) {
				t.Errorf("deploy = %d\nstdout: %s\nstderr: %s", code, &stdout, &stderr)
			}
			if reached.Load() != tt.reached {
				t.Errorf("server reached = %v, want %v", reached.Load(), tt.reached)
			}
		})
	}
}

func TestRunProbe(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	tests := []struct {
		args []string
		code int
	}{
		{[]string{srv.URL + "/health"}, 0},
		{[]string{"-status", "503", srv.URL + "/down"}, 0},
		{[]string{srv.URL + "/down"}, 3},
		{[]string{"-timeout", "2m", srv.URL}, 2},
		{nil, 2},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := realMain(context.Background(), append([]string{"probe"}, tt.args...), func(string) string { return "" }, &stdout, &stderr); code != tt.code {
			t.Errorf("probe %v = %d (%s%s), want %d", tt.args, code, &stdout, &stderr, tt.code)
		}
	}
}
