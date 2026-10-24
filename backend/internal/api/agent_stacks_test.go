package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel/tunneltest"
)

// newAgentStackTest is an agent environment whose stacks deploy through the tunnel to a
// fake agent: its control service runs a Local runner on agentDir with a fake compose.
func newAgentStackTest(t *testing.T, protocol int) (agentTest, stacks.Dir) {
	t.Helper()
	at := newAgentTestOpts(t, func(o *Options) {
		o.StacksDir = t.TempDir()
		envs := o.Envs
		o.StackRunner = func(env environments.Environment) stacks.Runner {
			return stacks.NewRemote(envs.ControlDialer(env.ID))
		}
	})
	agentDir := stacks.NewDir(t.TempDir())
	local := stacks.NewLocal(stacks.LocalOptions{Dir: agentDir, Exec: func(_ context.Context, cmd stacks.Command) (int, error) {
		_, _ = fmt.Fprintf(cmd.Stdout, "compose %s\n", strings.Join(cmd.Args[len(cmd.Args)-3:], " "))
		return 0, nil
	}})
	mux := http.NewServeMux()
	mux.Handle("/v1/stacks/", stacks.NewControl(local, nil))
	mux.Handle("/", fakeDockerAPI())
	if _, err := tunneltest.ConnectConfig(t, at.url, at.header, tunnel.Hello{Version: "v9.9.9", Hostname: "nas"}, mux, tunnel.Config{Protocol: protocol}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	at.waitOnline(t, true)
	return at, agentDir
}

func (at agentTest) stack(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	rec := do(t, at.h, request{method: method, path: fmt.Sprintf("/api/v1/environments/%d/stacks%s", at.envID, path), body: body, cookie: at.cookie})
	return rec.Code, rec.Body.String()
}

func TestAgentStacks(t *testing.T) {
	t.Parallel()
	at, agentDir := newAgentStackTest(t, tunnel.ProtocolVersion)
	if code, body := at.stack(t, "POST", "", `{"name":"web","compose":"services: {}\n","env":"A=1\n"}`); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	code, body := at.stack(t, "POST", "/web/up", `{"pull":true}`)
	if code != http.StatusOK {
		t.Fatalf("up = %d %s", code, body)
	}
	evs := stackEvents(t, body)
	if len(evs) != 2 || evs[0].Line != "compose --remove-orphans --pull always" || evs[1].ExitCode == nil || *evs[1].ExitCode != 0 {
		t.Errorf("up output = %+v", evs)
	}
	// The agent got the files before compose ran.
	if f, err := agentDir.Read("web"); err != nil || f.Env != "A=1\n" {
		t.Errorf("agent's copy = %+v, %v", f, err)
	}

	code, body = at.stack(t, "POST", "/web/down", `{"volumes":true}`)
	if evs := stackEvents(t, body); code != http.StatusOK || len(evs) != 2 || evs[0].Line != "compose down --remove-orphans --volumes" {
		t.Errorf("down = %d %+v", code, evs)
	}
}

func TestAgentStacksOldAgent(t *testing.T) {
	t.Parallel()
	at, _ := newAgentStackTest(t, 1)
	if code, body := at.stack(t, "POST", "", `{"name":"web","compose":"services: {}\n"}`); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	code, body := at.stack(t, "POST", "/web/up", "")
	if code != http.StatusConflict || !strings.Contains(body, "update the agent to use stacks") {
		t.Errorf("up on a protocol 1 agent = %d %s, want 409", code, body)
	}
	// Docker still works over protocol 1.
	rec := do(t, at.h, request{method: "GET", path: fmt.Sprintf("/api/v1/environments/%d/containers", at.envID), cookie: at.cookie})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"web"`) {
		t.Errorf("containers on a protocol 1 agent = %d %s", rec.Code, rec.Body)
	}
}

func TestAgentStacksOffline(t *testing.T) {
	t.Parallel()
	at := newAgentTestOpts(t, func(o *Options) {
		o.StacksDir = t.TempDir()
		envs := o.Envs
		o.StackRunner = func(env environments.Environment) stacks.Runner {
			return stacks.NewRemote(envs.ControlDialer(env.ID))
		}
	})
	if code, body := at.stack(t, "POST", "", `{"name":"web","compose":"services: {}\n"}`); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	if code, body := at.stack(t, "POST", "/web/up", ""); code != http.StatusServiceUnavailable {
		t.Errorf("up on an offline agent = %d %s, want 503", code, body)
	}
}
