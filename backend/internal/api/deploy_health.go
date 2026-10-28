package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/deployments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// deployKeepAlive is the pause after which a progress stream sends an empty line, so
// proxies keep a quiet stream (a long stability window) open.
const deployKeepAlive = 25 * time.Second

// deployOutput follows compose's output for the step: the first line about a container,
// network or volume ends the pulling.
func deployOutput(step func(deployments.Step)) func(stacks.Line) {
	deploying := false
	return func(l stacks.Line) {
		if deploying {
			return
		}
		t := strings.TrimSpace(l.Text)
		for _, p := range []string{"Container ", "Network ", "Volume "} {
			if strings.HasPrefix(t, p) {
				deploying = true
				step(deployments.StepDeploying)
				return
			}
		}
	}
}

// deployDeployment runs one deploy: the run's variables go into the stack's .env, the
// stack pulls and comes up (waiting for healthy services if set), then the health
// checks run. A failure puts the replaced values back, and with rollback on redeploys
// them. The images are recorded as user's updates. The outcome's error is a safe
// message naming the step; the log has the details.
func (s *server) deployDeployment(ctx context.Context, d deployments.Deployment, run deployments.Run, user string, report deployments.Report) deployments.Outcome {
	failed := func(err error) deployments.Outcome {
		s.log.Warn("deployment run", "deployment", d.Name, "run", run.ID, "err", err)
		return deployments.Outcome{Status: deployments.StatusFailed, Error: deployError(err)}
	}
	env, err := s.envs.Get(ctx, d.Environment)
	if err != nil {
		return failed(err)
	}
	c, err := s.envs.Client(env)
	if err != nil {
		return failed(err)
	}
	h := d.Health
	// Up and a rollback's up, each with its wait, then the stability window and the check.
	limit := 2*(stackTimeout+h.Wait()) + h.Stable() + probe.MaxTimeout + time.Minute
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var previous map[string]string
	var original string
	// A failed write or sync puts the .env back itself, before releasing the stack.
	runner, unlock, err := s.startStackEdit(ctx, env, d.Stack, func(f stacks.Files) stacks.Files {
		original = f.Env
		f.Env, previous = deployments.SetVariables(f.Env, run.Variables)
		return f
	})
	if err != nil {
		return failed(err)
	}
	defer unlock()

	step := deployments.StepPulling
	setStep := func(st deployments.Step) {
		step = st
		report(deployments.Progress{Step: st})
	}
	setStep(deployments.StepPulling)
	sr := stackRun{env: env, c: c, runner: runner, name: d.Stack, out: deployOutput(setStep), wait: h.Wait()}
	code, changes, err := s.pullAndDeploy(ctx, sr, user)
	if errors.Is(err, stacks.ErrNoWait) {
		report(deployments.Progress{Message: "the agent is too old to wait for healthy services; deployed without waiting", Warning: true})
		sr.wait = 0
		code, changes, err = s.pullAndDeploy(ctx, sr, user)
	}
	before, after := stackImages(changes)
	out := deployments.Outcome{Status: deployments.StatusSucceeded, Previous: previous, ImageBefore: before, ImageAfter: after}

	var problem string
	switch {
	case err != nil:
		s.log.Warn("deployment run", "deployment", d.Name, "run", run.ID, "err", err)
		problem = deployError(err)
	case code != 0:
		if step == deployments.StepPulling {
			problem = fmt.Sprintf("compose exited with code %d", code)
		} else {
			problem = s.composeFailure(ctx, c, d.Stack, code, sr.wait > 0)
		}
	default:
		setStep(deployments.StepChecking)
		problem = s.checkDeployment(ctx, runner, c, d, report)
	}
	if problem == "" {
		if h.RemoveOneOffs {
			s.removeOneOffs(ctx, c, d, run, report)
		}
		report(deployments.Progress{Message: "deployed"})
		return out
	}
	out.Status, out.Error = deployments.StatusFailed, fmt.Sprintf("%s: %s", step, problem)
	report(deployments.Progress{Message: out.Error})
	if len(run.Variables) == 0 {
		return out
	}
	if !h.Rollback || ctx.Err() != nil {
		// The next start of the stack (from the UI, an update, a webhook) uses the
		// values that worked, not the failed ones. The stack is still claimed.
		s.restoreEnv(env, d, run, original)
		return out
	}
	setStep(deployments.StepRollingBack)
	if err := s.rollBack(ctx, sr, d, run, original, user); err != nil {
		s.log.Warn("deployment run: roll back", "deployment", d.Name, "run", run.ID, "err", err)
		out.Error += "; the rollback failed too: " + deployError(err)
		report(deployments.Progress{Message: "the rollback failed: " + deployError(err)})
		return out
	}
	out.Status = deployments.StatusRolledBack
	out.Error += "; rolled back to the previous values"
	report(deployments.Progress{Message: "rolled back to the previous values"})
	return out
}

// removeOneOffs removes the containers of a succeeded run's stack that finished: exited
// with 0 and not restarted by Docker (a migration, a seed). Compose creates them again
// on the next up. A failure is a warning on the run; the caller holds the stack.
func (s *server) removeOneOffs(ctx context.Context, c docker.Client, d deployments.Deployment, run deployments.Run, report deployments.Report) {
	warn := func(msg string, err error) {
		s.log.Warn("deployment run: remove one-off containers", "deployment", d.Name, "run", run.ID, "err", err)
		report(deployments.Progress{Message: msg, Warning: true})
	}
	listCtx, cancel := context.WithTimeout(ctx, dockerTimeout)
	items, err := projectContainers(listCtx, c, d.Stack)
	cancel()
	if err != nil {
		warn("couldn't list the stack's containers to remove the finished one-off ones", err)
		return
	}
	var removed []string
	for _, it := range items {
		if it.State != container.StateExited || it.Labels[stacks.ProjectLabel] != d.Stack {
			continue
		}
		name := it.ID
		if len(it.Names) > 0 {
			name = strings.TrimPrefix(it.Names[0], "/")
		}
		ok, err := s.removeOneOff(ctx, c, it.ID)
		if err != nil {
			warn("couldn't remove the finished one-off container "+name, err)
		}
		if ok {
			removed = append(removed, name)
		}
	}
	if len(removed) > 0 {
		s.log.Info("deployment run: removed one-off containers", "deployment", d.Name, "run", run.ID, "containers", removed)
		report(deployments.Progress{Message: "removed finished one-off containers: " + strings.Join(removed, ", ")})
	}
}

// removeOneOff removes container id if it is a finished one-off; ok says it was removed.
// Without Force, the daemon refuses one that started again in between.
func (s *server) removeOneOff(ctx context.Context, c docker.Client, id string) (ok bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	info, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if docker.NotFound(err) || err == nil && !finishedOneOff(info.Container) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := c.ContainerRemove(ctx, id, client.ContainerRemoveOptions{}); err != nil && !docker.NotFound(err) {
		return false, err
	}
	return true, nil
}

// finishedOneOff is a container that exited with 0 and that Docker doesn't restart.
func finishedOneOff(info container.InspectResponse) bool {
	st := info.State
	if st == nil || st.Status != container.StateExited || st.Running || st.Restarting || st.ExitCode != 0 {
		return false
	}
	return info.HostConfig == nil || info.HostConfig.RestartPolicy.IsNone()
}

// errRollbackExit is a rollback whose compose up failed.
var errRollbackExit = &statusError{code: http.StatusBadGateway, msg: "compose failed on the previous values"}

// rollBack puts the .env a run replaced back and brings the stack up on it; the caller
// holds the stack.
func (s *server) rollBack(ctx context.Context, sr stackRun, d deployments.Deployment, run deployments.Run, original string, user string) error {
	s.restoreEnv(sr.env, d, run, original)
	f, err := s.stackDir(sr.env.ID).Read(d.Stack)
	if err != nil {
		return &internalFailure{msg: "read stack", err: err}
	}
	if err := sr.runner.Sync(ctx, d.Stack, f); err != nil {
		return fmt.Errorf("sync stack: %w", err)
	}
	code, _, err := s.pullAndDeploy(ctx, sr, user)
	if err != nil {
		return err
	}
	if code != 0 {
		return errRollbackExit
	}
	return nil
}

// composeFailure says why compose up failed, naming the first service in trouble when
// one is (with waiting, compose fails on an unhealthy or exited service).
func (s *server) composeFailure(ctx context.Context, c docker.Client, stack string, code int, waited bool) string {
	msg := fmt.Sprintf("compose exited with code %d", code)
	if !waited {
		return msg
	}
	states, err := s.serviceStates(ctx, c, stack)
	if err != nil {
		return msg
	}
	if p := states.problem(); p != "" {
		return p
	}
	return msg
}

// checkDeployment runs d's checks after compose up: every service's containers keep
// running for the stability window, then the HTTP check. It returns what failed, or "".
func (s *server) checkDeployment(ctx context.Context, runner stacks.Runner, c docker.Client, d deployments.Deployment, report deployments.Report) string {
	h := d.Health
	if h.StableSeconds > 0 {
		report(deployments.Progress{Message: fmt.Sprintf("watching the services for %ds", h.StableSeconds)})
		start, err := s.serviceStates(ctx, c, d.Stack)
		if err != nil {
			s.log.Warn("deployment check: list containers", "deployment", d.Name, "err", err)
			return "the stack's containers could not be listed"
		}
		if p := start.problem(); p != "" {
			return p
		}
		select {
		case <-s.after(h.Stable()):
		case <-ctx.Done():
			return "the run was cancelled or timed out"
		}
		end, err := s.serviceStates(ctx, c, d.Stack)
		if err != nil {
			s.log.Warn("deployment check: list containers", "deployment", d.Name, "err", err)
			return "the stack's containers could not be listed"
		}
		if p := end.changedSince(start); p != "" {
			return p
		}
		report(deployments.Progress{Message: "the services stayed up"})
	}
	if h.HTTP == nil {
		return ""
	}
	svc := h.HTTP.Service
	id, err := s.serviceContainer(ctx, c, d.Stack, svc)
	if err != nil {
		s.log.Warn("deployment check: list containers", "deployment", d.Name, "err", err)
		return "the stack's containers could not be listed"
	}
	if id == "" {
		return fmt.Sprintf("service %s has no running container for the HTTP check", svc)
	}
	p, ok := runner.(stacks.Prober)
	if !ok {
		report(deployments.Progress{Service: svc, Message: "this environment can't run HTTP checks; the check was skipped", Warning: true})
		return ""
	}
	report(deployments.Progress{Service: svc, Message: fmt.Sprintf("HTTP check of port %d%s", h.HTTP.Port, h.HTTP.Path)})
	err = p.Probe(ctx, d.Stack, h.HTTP.Request(id))
	var fe *probe.FailedError
	switch {
	case err == nil:
		report(deployments.Progress{Service: svc, Message: "HTTP check passed"})
		return ""
	case errors.Is(err, stacks.ErrNoProbe):
		report(deployments.Progress{Service: svc, Message: "the agent is too old for HTTP checks; the check was skipped", Warning: true})
		return ""
	case errors.As(err, &fe):
		return fmt.Sprintf("service %s: HTTP check: %s", svc, fe.Msg)
	case errors.Is(err, probe.ErrNoSelf):
		return fmt.Sprintf("service %s: HTTP check: %s", svc, probe.ErrNoSelf.Error())
	default:
		s.log.Warn("deployment check: http", "deployment", d.Name, "service", svc, "err", err)
		return fmt.Sprintf("service %s: the HTTP check could not run (see the server log)", svc)
	}
}

// serviceContainer returns the ID of a running container of stack's service svc, or "".
func (s *server) serviceContainer(ctx context.Context, c docker.Client, stack, svc string) (string, error) {
	lctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	items, err := projectContainers(lctx, c, stack)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Labels[stacks.ServiceLabel] == svc && it.State == container.StateRunning {
			return it.ID, nil
		}
	}
	return "", nil
}

// containerState is what the stability window compares of a container.
type containerState struct {
	service   string
	state     container.ContainerState
	exitCode  int
	restarts  int
	unhealthy bool
}

// serviceStates are a stack's containers by ID.
type serviceStates map[string]containerState

// serviceStates inspects every container of stack.
func (s *server) serviceStates(ctx context.Context, c docker.Client, stack string) (serviceStates, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	items, err := projectContainers(ctx, c, stack)
	if err != nil {
		return nil, err
	}
	out := serviceStates{}
	for _, it := range items {
		info, err := c.ContainerInspect(ctx, it.ID, client.ContainerInspectOptions{})
		if docker.NotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		st := containerState{service: it.Labels[stacks.ServiceLabel], state: it.State, restarts: info.Container.RestartCount}
		if cs := info.Container.State; cs != nil {
			st.state, st.exitCode = cs.Status, cs.ExitCode
			st.unhealthy = cs.Health != nil && cs.Health.Status == container.Unhealthy
		}
		out[it.ID] = st
	}
	return out, nil
}

// problem names the first service (by name) with a container that isn't up: exited
// with an error, restarting, dead or unhealthy. A container that exited with 0 is a
// one-off job that finished.
func (ss serviceStates) problem() string {
	for _, id := range ss.byService() {
		st := ss[id]
		switch {
		case st.unhealthy:
			return fmt.Sprintf("service %s: a container is unhealthy", st.service)
		case st.state == container.StateRunning:
		case st.state == container.StateExited && st.exitCode == 0:
		case st.state == container.StateExited:
			return fmt.Sprintf("service %s: a container exited with code %d", st.service, st.exitCode)
		default:
			return fmt.Sprintf("service %s: a container is %s", st.service, st.state)
		}
	}
	return ""
}

// changedSince names the first service whose running containers stopped, went away or
// restarted since start, or that has a problem now.
func (ss serviceStates) changedSince(start serviceStates) string {
	for _, id := range start.byService() {
		was := start[id]
		if was.state != container.StateRunning {
			continue
		}
		now, ok := ss[id]
		switch {
		case !ok:
			return fmt.Sprintf("service %s: a container went away during the stability window", was.service)
		case now.restarts > was.restarts:
			return fmt.Sprintf("service %s: a container restarted during the stability window", was.service)
		}
	}
	return ss.problem()
}

// byService lists the container IDs by service name, then ID, so messages are stable.
func (ss serviceStates) byService() []string {
	ids := make([]string, 0, len(ss))
	for id := range ss {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := strings.Compare(ss[a].service, ss[b].service); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return ids
}

// deployEvent is a line of a deploy's progress stream: progress, then the final line
// with the run's status.
type deployEvent struct {
	Step    string `json:"step,omitempty"`
	Service string `json:"service,omitempty"`
	Message string `json:"message,omitempty"`
	Warning bool   `json:"warning,omitempty"`
	// Status, Run and Error are on the final line only.
	Status   string   `json:"status,omitempty"`
	Run      int64    `json:"run,omitempty"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// streamDeploy answers a deploy with ?stream=1: the run's progress as JSON lines until it
// ends, then a line with its status. The answer is 200 once streaming starts; CI reads
// the last line. Leaving early doesn't stop the run.
func (s *server) streamDeploy(w http.ResponseWriter, r *http.Request, c deployCaller, ticket deployments.Ticket) {
	progress, stop := s.deployments.Follow(ticket)
	defer stop()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	send := func(ev deployEvent) bool {
		return enc.Encode(ev) == nil && rc.Flush() == nil
	}
	send(deployEvent{Run: ticket.RunID, Message: "queued"})
	for open := true; open; {
		select {
		case p, ok := <-progress:
			if !ok {
				open = false
				break
			}
			if !send(deployEvent{Step: string(p.Step), Service: p.Service, Message: p.Message, Warning: p.Warning}) {
				return
			}
		case <-s.after(deployKeepAlive):
			if !send(deployEvent{}) {
				return
			}
		case <-r.Context().Done():
			return
		case <-s.done.Done():
			send(deployEvent{Run: ticket.RunID, Error: "nudge is shutting down; the run's result is in the deployment's runs"})
			return
		}
	}
	run, err := s.deployments.Run(context.WithoutCancel(r.Context()), c.d.ID, ticket.RunID)
	if err != nil {
		s.log.Error("deployment run", "deployment", c.d.Name, "run", ticket.RunID, "err", err)
		send(deployEvent{Run: ticket.RunID, Error: "the run's result could not be read"})
		return
	}
	send(deployEvent{Status: string(run.Status), Run: run.ID, Step: string(run.Step), Error: run.Error, Warnings: run.Warnings})
}
