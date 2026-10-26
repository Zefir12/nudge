package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

const (
	// createTimeout bounds a container create (and its start, if asked).
	createTimeout = 2 * time.Minute
	// recreateTimeout bounds a recreate without its pull: a stop, a create, a start and
	// a remove, and the same again to roll back.
	recreateTimeout = 5 * time.Minute
	// oldSuffix is added to a container's name while its replacement is created.
	oldSuffix = "-nudge-old"
)

// containerSpec is what the create form edits: nudge's own subset of Docker's container
// settings. A recreate keeps every setting it doesn't cover.
type containerSpec struct {
	Image string `json:"image"`
	Name  string `json:"name"`
	// Command replaces the image's CMD; empty keeps it.
	Command       []string          `json:"command"`
	Env           []string          `json:"env"`
	Ports         []portSpec        `json:"ports"`
	Mounts        []mountSpec       `json:"mounts"`
	Networks      []string          `json:"networks"`
	RestartPolicy string            `json:"restart_policy"`
	Labels        map[string]string `json:"labels"`
}

type portSpec struct {
	// HostIP empty publishes on every address; HostPort 0 picks a free port.
	HostIP        string `json:"host_ip"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

type mountSpec struct {
	// Type is "volume" (Source a volume name, empty for a new anonymous one) or "bind"
	// (Source an absolute host path).
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type createContainerRequest struct {
	containerSpec
	Start bool `json:"start"`
}

type recreateContainerRequest struct {
	// Pull pulls the image (with its registry's stored credentials) first, so the new
	// container runs its latest version.
	Pull bool `json:"pull"`
	// Spec, if set, replaces the settings it covers; nil keeps them all.
	Spec *containerSpec `json:"spec"`
}

type createContainerResponse struct {
	ID       string   `json:"id"`
	Warnings []string `json:"warnings"`
}

// fieldsResponse is a 400 for invalid settings, with a message per field ("image",
// "ports.0.host_port") for the form.
type fieldsResponse struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields"`
}

// statusError is returned by a Docker call to answer code with msg.
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

var restartPolicies = []string{"", "no", "always", "unless-stopped", "on-failure"}

// validate returns a message per invalid field, keyed like the JSON (ports.0.host_port).
func (sp *containerSpec) validate() map[string]string {
	errs := map[string]string{}
	sp.Image = strings.TrimSpace(sp.Image)
	sp.Name = strings.TrimPrefix(strings.TrimSpace(sp.Name), "/")
	if sp.Image == "" {
		errs["image"] = "required"
	} else if !validPullRef(sp.Image) && !digest.MatchString(sp.Image) {
		errs["image"] = "must be a reference such as nginx:1.29, or an image ID"
	}
	if sp.Name != "" && (!objectID.MatchString(sp.Name) || len(sp.Name) < 2) {
		errs["name"] = "2 or more letters, digits, '_', '.' or '-', starting with a letter or digit"
	}
	for i, e := range sp.Env {
		if k, _, _ := strings.Cut(e, "="); strings.TrimSpace(k) == "" {
			errs[fmt.Sprintf("env.%d", i)] = "must be NAME=value"
		}
	}
	for i, p := range sp.Ports {
		key := func(f string) string { return fmt.Sprintf("ports.%d.%s", i, f) }
		if p.ContainerPort < 1 || p.ContainerPort > 65535 {
			errs[key("container_port")] = "must be 1 to 65535"
		}
		if p.HostPort < 0 || p.HostPort > 65535 {
			errs[key("host_port")] = "must be 0 (any free port) to 65535"
		}
		if !slices.Contains([]string{"", "tcp", "udp", "sctp"}, p.Protocol) {
			errs[key("protocol")] = "must be tcp, udp or sctp"
		}
		if _, err := netip.ParseAddr(p.HostIP); p.HostIP != "" && err != nil {
			errs[key("host_ip")] = "must be an IP address"
		}
	}
	for i, m := range sp.Mounts {
		key := func(f string) string { return fmt.Sprintf("mounts.%d.%s", i, f) }
		if !path.IsAbs(m.Target) || path.Clean(m.Target) == "/" {
			errs[key("target")] = "must be an absolute path in the container, not /"
		}
		switch m.Type {
		case "volume":
			if m.Source != "" && !objectID.MatchString(m.Source) {
				errs[key("source")] = "must be a volume name, or empty for a new anonymous volume"
			}
		case "bind":
			if !path.IsAbs(m.Source) {
				errs[key("source")] = "must be an absolute path on the host"
			}
		default:
			errs[key("type")] = "must be volume or bind"
		}
	}
	for i, n := range sp.Networks {
		if !objectID.MatchString(n) {
			errs[fmt.Sprintf("networks.%d", i)] = "must be a network name"
		}
	}
	if !slices.Contains(restartPolicies, sp.RestartPolicy) {
		errs["restart_policy"] = "must be no, always, unless-stopped or on-failure"
	}
	if hasEmptyKey(sp.Labels) {
		errs["labels"] = "label names must not be empty"
	}
	return errs
}

// apply writes the spec into Docker's settings; the ones it doesn't cover stay as they
// are. old holds the endpoint settings of a container being recreated (nil on create).
func (sp *containerSpec) apply(cfg *container.Config, hc *container.HostConfig, nc *network.NetworkingConfig, old map[string]*network.EndpointSettings) {
	cfg.Image = sp.Image
	cfg.Cmd = sp.Command
	cfg.Env = sp.Env
	cfg.Labels = sp.Labels

	hc.PortBindings = network.PortMap{}
	for _, p := range sp.Ports {
		proto := network.IPProtocol(p.Protocol)
		if proto == "" {
			proto = network.TCP
		}
		// validate checked the range.
		port, _ := network.PortFrom(uint16(p.ContainerPort), proto) // #nosec G115 -- 1 to 65535
		b := network.PortBinding{}
		if p.HostIP != "" {
			b.HostIP = netip.MustParseAddr(p.HostIP)
		}
		if p.HostPort != 0 {
			b.HostPort = strconv.Itoa(p.HostPort)
		}
		hc.PortBindings[port] = append(hc.PortBindings[port], b)
		if cfg.ExposedPorts == nil {
			cfg.ExposedPorts = network.PortSet{}
		}
		cfg.ExposedPorts[port] = struct{}{}
	}

	// Volumes and binds are replaced; other mounts (tmpfs, images) are kept. A mount the
	// spec keeps (same source and target) keeps its options (SELinux labels, propagation,
	// volume options).
	oldBinds := map[[2]string][]string{}
	for _, b := range hc.Binds {
		if parts := strings.Split(b, ":"); len(parts) >= 2 {
			oldBinds[[2]string{parts[0], path.Clean(parts[1])}] = parts[2:]
		}
	}
	oldMounts := map[[2]string]mount.Mount{}
	for _, m := range hc.Mounts {
		oldMounts[[2]string{m.Source, path.Clean(m.Target)}] = m
	}
	hc.Binds = nil
	hc.Mounts = slices.DeleteFunc(hc.Mounts, func(m mount.Mount) bool {
		return m.Type == mount.TypeVolume || m.Type == mount.TypeBind
	})
	for _, m := range sp.Mounts {
		key := [2]string{m.Source, path.Clean(m.Target)}
		if opts, ok := oldBinds[key]; ok && m.Source != "" {
			hc.Binds = append(hc.Binds, bindWithMode(key, opts, m.ReadOnly))
			continue
		}
		nm, ok := oldMounts[key]
		if !ok || string(nm.Type) != m.Type {
			nm = mount.Mount{Type: mount.Type(m.Type), Source: m.Source, Target: key[1]}
		}
		nm.ReadOnly = m.ReadOnly
		hc.Mounts = append(hc.Mounts, nm)
	}

	if sp.RestartPolicy != string(hc.RestartPolicy.Name) {
		hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(sp.RestartPolicy)}
	}

	// Without networks the container keeps its own (or gets Docker's default bridge).
	if len(sp.Networks) > 0 {
		hc.NetworkMode = container.NetworkMode(sp.Networks[0])
		nc.EndpointsConfig = map[string]*network.EndpointSettings{}
		for _, n := range sp.Networks {
			ep := &network.EndpointSettings{}
			if o, ok := old[n]; ok {
				ep = o
			}
			nc.EndpointsConfig[n] = ep
		}
	}
}

// bindWithMode rebuilds a "source:target:options" bind with ro or rw as asked.
func bindWithMode(key [2]string, opts []string, readOnly bool) string {
	var out []string
	for _, o := range opts {
		for _, part := range strings.Split(o, ",") {
			if part != "" && part != "ro" && part != "rw" {
				out = append(out, part)
			}
		}
	}
	if readOnly {
		out = append(out, "ro")
	}
	b := key[0] + ":" + key[1]
	if len(out) > 0 {
		b += ":" + strings.Join(out, ",")
	}
	return b
}

// dropImageDefaults removes from cfg what the container's image sets (its environment,
// command, labels, volumes, ports, …), as Docker copies them into every container. A
// recreate then takes them from the image it runs, which may be newer, and the spec
// shows only the container's own settings.
func dropImageDefaults(cfg *container.Config, img *dockerspec.DockerOCIImageConfig) {
	if img == nil {
		return
	}
	cfg.Env = slices.DeleteFunc(slices.Clone(cfg.Env), func(e string) bool { return slices.Contains(img.Env, e) })
	if slices.Equal(cfg.Cmd, img.Cmd) {
		cfg.Cmd = nil
	}
	if slices.Equal(cfg.Entrypoint, img.Entrypoint) {
		cfg.Entrypoint = nil
	}
	if cfg.WorkingDir == img.WorkingDir {
		cfg.WorkingDir = ""
	}
	if cfg.User == img.User {
		cfg.User = ""
	}
	if cfg.StopSignal == img.StopSignal {
		cfg.StopSignal = ""
	}
	if hc, ihc := cfg.Healthcheck, img.Healthcheck; hc != nil && ihc != nil && slices.Equal(hc.Test, ihc.Test) &&
		hc.Interval == ihc.Interval && hc.Timeout == ihc.Timeout && hc.Retries == ihc.Retries {
		cfg.Healthcheck = nil
	}
	cfg.Labels = maps.Clone(cfg.Labels)
	maps.DeleteFunc(cfg.Labels, func(k, v string) bool { iv, ok := img.Labels[k]; return ok && iv == v })
	cfg.Volumes = maps.Clone(cfg.Volumes)
	maps.DeleteFunc(cfg.Volumes, func(k string, _ struct{}) bool { _, ok := img.Volumes[k]; return ok })
	cfg.ExposedPorts = maps.Clone(cfg.ExposedPorts)
	maps.DeleteFunc(cfg.ExposedPorts, func(p network.Port, _ struct{}) bool { _, ok := img.ExposedPorts[p.String()]; return ok })
}

// imageConfig returns the config of the image a container was created from, or nil if
// it is gone (then the container's settings are used as they are).
func imageConfig(ctx context.Context, c docker.Client, id string) (*dockerspec.DockerOCIImageConfig, error) {
	res, err := c.ImageInspect(ctx, id)
	if docker.NotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect image: %w", err)
	}
	return res.Config, nil
}

// toSpec reads a container's settings in the spec's shape.
func toSpec(ct container.InspectResponse) containerSpec {
	sp := containerSpec{
		Name: strings.TrimPrefix(ct.Name, "/"), Ports: []portSpec{}, Mounts: []mountSpec{},
		Command: []string{}, Env: []string{}, Networks: []string{}, Labels: map[string]string{},
	}
	if cfg := ct.Config; cfg != nil {
		sp.Image, sp.Command, sp.Env, sp.Labels = cfg.Image, nonNil(cfg.Cmd), nonNil(cfg.Env), nonNilMap(cfg.Labels)
	}
	if hc := ct.HostConfig; hc != nil {
		sp.RestartPolicy = string(hc.RestartPolicy.Name)
		ports := slices.Collect(maps.Keys(hc.PortBindings))
		slices.SortFunc(ports, func(a, b network.Port) int {
			return cmp.Or(cmp.Compare(a.Num(), b.Num()), cmp.Compare(a.Proto(), b.Proto()))
		})
		for _, port := range ports {
			for _, b := range hc.PortBindings[port] {
				ps := portSpec{ContainerPort: int(port.Num()), Protocol: string(port.Proto())}
				if b.HostIP.IsValid() && !b.HostIP.IsUnspecified() {
					ps.HostIP = b.HostIP.String()
				}
				ps.HostPort, _ = strconv.Atoi(b.HostPort)
				sp.Ports = append(sp.Ports, ps)
			}
		}
		if mode := hc.NetworkMode; mode.IsHost() || mode.IsNone() {
			sp.Networks = append(sp.Networks, string(mode))
		}
	}
	for _, m := range ct.Mounts {
		switch m.Type {
		case mount.TypeVolume:
			sp.Mounts = append(sp.Mounts, mountSpec{Type: "volume", Source: m.Name, Target: m.Destination, ReadOnly: !m.RW})
		case mount.TypeBind:
			sp.Mounts = append(sp.Mounts, mountSpec{Type: "bind", Source: m.Source, Target: m.Destination, ReadOnly: !m.RW})
		}
	}
	if ns := ct.NetworkSettings; ns != nil && len(sp.Networks) == 0 {
		sp.Networks = append(sp.Networks, sortedKeys(ns.Networks)...)
	}
	return sp
}

func (s *server) decodeSpec(w http.ResponseWriter, sp *containerSpec) bool {
	if errs := sp.validate(); len(errs) > 0 {
		s.writeJSON(w, http.StatusBadRequest, fieldsResponse{Error: "invalid container settings", Fields: errs})
		return false
	}
	return true
}

// createContainer creates a container from {spec, "start": true} and answers 201. If it
// doesn't start, it is removed again, so a failed request leaves nothing behind.
func (s *server) createContainer(w http.ResponseWriter, r *http.Request) {
	var req createContainerRequest
	if !s.decodeJSON(w, r, &req) || !s.decodeSpec(w, &req.containerSpec) {
		return
	}
	auditTarget(r, req.Name)
	resp, ok := s.runDocker(context.WithoutCancel(r.Context()), w, r, "container create", "", createTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		cfg, hc, nc := &container.Config{}, &container.HostConfig{}, &network.NetworkingConfig{}
		req.apply(cfg, hc, nc, nil)
		res, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{Name: req.Name, Config: cfg, HostConfig: hc, NetworkingConfig: nc})
		if err != nil {
			return nil, refusal(err, "created", req.Env)
		}
		if req.Start {
			if _, err := c.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
				s.cleanUp(ctx, c, res.ID)
				return nil, refusal(err, "started (it was removed again)", req.Env)
			}
		}
		return createContainerResponse{ID: res.ID, Warnings: nonNil(res.Warnings)}, nil
	})
	if ok {
		s.writeJSON(w, http.StatusCreated, resp)
	}
}

func (s *server) getContainerSpec(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	s.dockerCall(w, r, "container", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		ct := res.Container
		if ct.Config != nil {
			img, err := imageConfig(ctx, c, ct.Image)
			if err != nil {
				return nil, err
			}
			cfg := *ct.Config
			dropImageDefaults(&cfg, img)
			ct.Config = &cfg
		}
		return toSpec(ct), nil
	})
}

// recreateContainer replaces a container with a new one of the same (or changed)
// settings: optional pull, stop, rename the old one aside, create, start if it was
// running, remove the old one. If the create or start fails the new container is
// removed and the old one is renamed back and started again.
func (s *server) recreateContainer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	var req recreateContainerRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Spec != nil && !s.decodeSpec(w, req.Spec) {
		return
	}
	rp, ok := s.beginReplace(w, r, id)
	if !ok {
		return
	}
	defer rp.done()
	ref := rp.old.Config.Image
	if req.Spec != nil {
		ref = req.Spec.Image
	}
	if req.Pull && (digest.MatchString(ref) || !validPullRef(ref)) {
		s.writeError(w, http.StatusBadRequest, "the image is an ID, not a reference such as nginx:1.29, so it can't be pulled")
		return
	}
	if req.Pull && !s.pullForReplace(w, r, rp, ref) {
		return
	}
	newID, ok := s.finishReplace(w, r, rp, req.Spec)
	if !ok {
		return
	}
	s.log.Info("container recreate", "env", rp.env.ID, "container", rp.old.ID, "new", newID, "pull", req.Pull, "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, createContainerResponse{ID: newID, Warnings: []string{}})
}

// replacement is a recreate or update under way: the container and the image it runs
// (zero if the image is gone), inspected before anything changed.
type replacement struct {
	env      environments.Environment
	c        docker.Client
	old      container.InspectResponse
	oldImage image.InspectResponse
	done     func()
}

// errRecreating refuses a second recreate or update of a container while one runs.
var errRecreating = &statusError{code: http.StatusConflict, msg: "this container is already being recreated"}

// pullFailure is a pull the daemon refused or that broke off, with the registry
// password to mask in its message.
type pullFailure struct {
	err      error
	password string
}

func (e *pullFailure) Error() string { return "pull: " + e.err.Error() }
func (e *pullFailure) Unwrap() error { return e.err }

// internalFailure is nudge's own failure (the database, the key), answered 500.
type internalFailure struct {
	msg string
	err error
}

func (e *internalFailure) Error() string { return e.msg + ": " + e.err.Error() }
func (e *internalFailure) Unwrap() error { return e.err }

// dockerFailure is a failed Docker call that names no container: answered like any
// Docker error, not as a missing container.
type dockerFailure struct {
	msg string
	err error
}

func (e *dockerFailure) Error() string { return e.msg + ": " + e.err.Error() }
func (e *dockerFailure) Unwrap() error { return e.err }

// beginReplace inspects container id and its image and claims it for one recreate at a
// time; on false it has answered. The caller defers done.
func (s *server) beginReplace(w http.ResponseWriter, r *http.Request, id string) (*replacement, bool) {
	env, c, ok := s.environment(w, r)
	if !ok {
		return nil, false
	}
	rp, err := s.startReplace(r.Context(), env, c, id)
	if err != nil {
		s.replaceError(w, r, env, err)
		return nil, false
	}
	return rp, true
}

// startReplace inspects container id (an ID or a name) and its image and claims it for
// one recreate at a time. The caller defers done.
func (s *server) startReplace(ctx context.Context, env environments.Environment, c docker.Client, id string) (*replacement, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	res, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	old := res.Container
	if old.Config == nil || old.HostConfig == nil || old.State == nil {
		return nil, errors.New("inspect: incomplete container settings")
	}
	// Before a pull, while the old image is surely still there.
	var oldImage image.InspectResponse
	ires, err := c.ImageInspect(ctx, old.Image)
	switch {
	case docker.NotFound(err):
	case err != nil:
		return nil, fmt.Errorf("inspect image: %w", err)
	default:
		oldImage = ires.InspectResponse
	}
	done, ok := s.beginRecreate(env.ID, old.ID)
	if !ok {
		return nil, errRecreating
	}
	return &replacement{env: env, c: c, old: old, oldImage: oldImage, done: done}, nil
}

// pullForReplace pulls ref with its registry's credentials; on false it has answered.
func (s *server) pullForReplace(w http.ResponseWriter, r *http.Request, rp *replacement, ref string) bool {
	if err := s.pullWithAuth(r.Context(), rp.c, ref); err != nil {
		s.replaceError(w, r, rp.env, err)
		return false
	}
	return true
}

// pullWithAuth pulls ref with its registry's credentials and waits for the pull.
func (s *server) pullWithAuth(ctx context.Context, c docker.Client, ref string) error {
	opts, password, err := s.registryAuth(ctx, ref)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	if err := pullAndWait(ctx, c, ref, opts); err != nil {
		return &pullFailure{err: err, password: password}
	}
	return nil
}

// finishReplace replaces the container (with spec's settings, if not nil) and returns
// the new one's ID; on false it has answered.
func (s *server) finishReplace(w http.ResponseWriter, r *http.Request, rp *replacement, spec *containerSpec) (string, bool) {
	newID, err := s.completeReplace(r.Context(), rp, spec)
	if err != nil {
		s.replaceError(w, r, rp.env, err)
		return "", false
	}
	return newID, true
}

// completeReplace replaces the container (with spec's settings, if not nil) and returns
// the new one's ID. The pull has its own timeout; the steps that change containers start
// now and don't stop when ctx ends (a client going away, a shutdown).
func (s *server) completeReplace(ctx context.Context, rp *replacement, spec *containerSpec) (string, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recreateTimeout)
	defer cancel()
	newID, err := s.replace(rctx, rp.c, rp.old, rp.oldImage.Config, spec)
	if err != nil {
		return "", err
	}
	s.envs.Seen(rctx, &rp.env)
	return newID, nil
}

// replaceError answers a failed step of a recreate or update.
func (s *server) replaceError(w http.ResponseWriter, r *http.Request, env environments.Environment, err error) {
	var pf *pullFailure
	var inf *internalFailure
	var df *dockerFailure
	switch {
	case errors.Is(err, errRegistryDecrypt):
		s.log.Error("registry credentials", "err", err)
		s.writeError(w, http.StatusInternalServerError, "the stored registry password can't be decrypted (was secret.key replaced?): enter it again under Registries")
	case errors.As(err, &inf):
		s.internalError(w, inf.msg, inf.err)
	case errors.As(err, &pf):
		s.pullError(w, r, env, pf.err, pf.password)
	case errors.As(err, &df):
		s.dockerError(w, r, env, df.msg, df.err)
	default:
		s.recreateError(w, r, env, err)
	}
}

// pullAndWait pulls ref and waits for the pull to finish.
func pullAndWait(ctx context.Context, c docker.Client, ref string, opts client.ImagePullOptions) error {
	resp, err := c.ImagePull(ctx, ref, opts)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Close() }()
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return err
		}
		if msg.Error != nil {
			return errors.New(msg.Error.Message)
		}
	}
	return nil
}

// replace does the steps of a recreate after the pull and returns the new container's ID.
func (s *server) replace(ctx context.Context, c docker.Client, old container.InspectResponse, img *dockerspec.DockerOCIImageConfig, spec *containerSpec) (string, error) {
	name := strings.TrimPrefix(old.Name, "/")
	cfg, hc, nc := recreateSettings(old)
	dropImageDefaults(cfg, img)
	if spec != nil {
		spec.apply(cfg, hc, nc, nc.EndpointsConfig)
		if spec.Name != "" {
			name = spec.Name
		}
	}
	keepAnonymousVolumes(old, hc)
	var secrets []string
	if spec != nil {
		secrets = spec.Env
	}
	secrets = append(secrets, old.Config.Env...)

	wasRunning := old.State.Running || old.State.Restarting || old.State.Paused
	if wasRunning {
		if _, err := c.ContainerStop(ctx, old.ID, client.ContainerStopOptions{}); err != nil {
			// It may have stopped anyway; make sure it runs again.
			rctx, cancel := rollbackContext(ctx)
			defer cancel()
			if _, serr := c.ContainerStart(rctx, old.ID, client.ContainerStartOptions{}); serr != nil {
				s.log.Error("container recreate: start old container", "container", old.ID, "err", serr)
			}
			return "", fmt.Errorf("stop: %w", err)
		}
	}
	oldName := strings.TrimPrefix(old.Name, "/")
	aside := oldName + oldSuffix
	if _, err := c.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: aside}); err != nil {
		_ = s.restore(ctx, c, old, "", wasRunning) // logs its own failures
		if docker.Conflict(err) {
			return "", &statusError{code: http.StatusConflict, msg: fmt.Sprintf("a container named %s exists (left by an earlier recreate?): remove or rename it first", aside)}
		}
		return "", fmt.Errorf("rename: %w", err)
	}
	res, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: hc, NetworkingConfig: nc})
	if err != nil {
		return "", refusal(err, "created"+s.restoreNote(ctx, c, old, "", wasRunning), secrets)
	}
	if wasRunning {
		if _, err := c.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
			return "", refusal(err, "started"+s.restoreNote(ctx, c, old, res.ID, wasRunning), secrets)
		}
	}
	if _, err := c.ContainerRemove(ctx, old.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		// The new container runs; the old one only has to be removed by hand.
		s.log.Warn("container recreate: remove old container", "container", old.ID, "name", aside, "err", err)
	}
	return res.ID, nil
}

// restoreNote restores the old container and says how that went, for the answer.
func (s *server) restoreNote(ctx context.Context, c docker.Client, old container.InspectResponse, newID string, start bool) string {
	if err := s.restore(ctx, c, old, newID, start); err != nil {
		return fmt.Sprintf(" (and restoring the old one failed: it may still be stopped or named %s%s, check it by hand)", strings.TrimPrefix(old.Name, "/"), oldSuffix)
	}
	return " (the old one was restored)"
}

// restore undoes a failed recreate: removes the new container, if any, gives the old one
// its name back and starts it if it ran. It runs on a fresh timeout, so a step that used
// up the recreate's still gets undone. Failures are logged and returned.
func (s *server) restore(ctx context.Context, c docker.Client, old container.InspectResponse, newID string, start bool) error {
	ctx, cancel := rollbackContext(ctx)
	defer cancel()
	if newID != "" {
		s.cleanUp(ctx, c, newID)
	}
	var errs []error
	if _, err := c.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: strings.TrimPrefix(old.Name, "/")}); err != nil && !docker.InvalidArgument(err) {
		s.log.Error("container recreate: rename back", "container", old.ID, "err", err)
		errs = append(errs, err)
	}
	if start {
		if _, err := c.ContainerStart(ctx, old.ID, client.ContainerStartOptions{}); err != nil {
			s.log.Error("container recreate: start old container", "container", old.ID, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// rollbackContext bounds the undo steps of a recreate on their own, keeping ctx's values.
func rollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
}

// beginRecreate marks a container as being recreated; ok is false while another recreate
// of it runs. The set holds only running recreates, so it stays small.
func (s *server) beginRecreate(env int64, id string) (done func(), ok bool) {
	key := fmt.Sprintf("%d/%s", env, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recreating[key] {
		return nil, false
	}
	s.recreating[key] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.recreating, key)
	}, true
}

// cleanUp removes a container nudge just created and couldn't use.
func (s *server) cleanUp(ctx context.Context, c docker.Client, id string) {
	if _, err := c.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil {
		s.log.Error("remove failed new container", "container", id, "err", err)
	}
}

// recreateSettings copies a container's settings for its replacement, leaving out what
// Docker generated for it: the hostname (its short ID) and the endpoints' addresses.
func recreateSettings(old container.InspectResponse) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	cfg, hc := *old.Config, *old.HostConfig
	if strings.HasPrefix(old.ID, cfg.Hostname) && cfg.Hostname != "" {
		cfg.Hostname = ""
	}
	hc.Mounts = slices.Clone(hc.Mounts)
	hc.Binds = slices.Clone(hc.Binds)
	cfg.ExposedPorts = maps.Clone(cfg.ExposedPorts)
	nc := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	if ns := old.NetworkSettings; ns != nil {
		for name, ep := range ns.Networks {
			if ep == nil {
				continue
			}
			nc.EndpointsConfig[name] = &network.EndpointSettings{
				IPAMConfig: ep.IPAMConfig, Links: ep.Links, Aliases: ep.Aliases,
				DriverOpts: ep.DriverOpts, GwPriority: ep.GwPriority,
			}
		}
	}
	return &cfg, &hc, nc
}

// keepAnonymousVolumes mounts the old container's anonymous volumes (from an image's
// VOLUME, `-v /path` or a volume mount without a source) at the same paths, so their data
// survives a recreate.
func keepAnonymousVolumes(old container.InspectResponse, hc *container.HostConfig) {
	anon := map[string]container.MountPoint{}
	for _, m := range old.Mounts {
		if m.Type == mount.TypeVolume {
			anon[path.Clean(m.Destination)] = m
		}
	}
	mounted := map[string]bool{}
	for i, m := range hc.Mounts {
		target := path.Clean(m.Target)
		if om, ok := anon[target]; ok && m.Type == mount.TypeVolume && m.Source == "" {
			hc.Mounts[i].Source = om.Name
		}
		mounted[target] = true
	}
	for _, b := range hc.Binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 {
			mounted[path.Clean(parts[1])] = true
		}
	}
	for _, m := range old.Mounts {
		// Named volumes have a mount or bind of their own.
		if m.Type != mount.TypeVolume || mounted[path.Clean(m.Destination)] {
			continue
		}
		hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeVolume, Source: m.Name, Target: m.Destination, ReadOnly: !m.RW})
	}
}

// refusal turns a failed create or start into an answer that explains it: the daemon's
// message (such as "port is already allocated" or "No such image"), with the values of
// the container's environment masked, since they often hold secrets. A lost connection
// or a timeout stays as is.
func refusal(err error, step string, env []string) error {
	if docker.Unreachable(err) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	msg := strings.TrimPrefix(err.Error(), "Error response from daemon: ")
	var values []string
	for _, e := range env {
		if _, v, ok := strings.Cut(e, "="); ok && len(v) >= 3 {
			values = append(values, v)
		}
	}
	// Longest first, so a value that contains another is masked whole.
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	for _, v := range values {
		msg = maskSecret(msg, v)
	}
	code := http.StatusBadRequest
	if docker.Conflict(err) {
		code = http.StatusConflict
	}
	return &statusError{code: code, msg: "the container could not be " + step + ": " + msg}
}

func (s *server) recreateError(w http.ResponseWriter, r *http.Request, env environments.Environment, err error) {
	var se *statusError
	switch {
	case errors.As(err, &se):
		s.envs.Seen(r.Context(), &env)
		s.writeError(w, se.code, se.msg)
	case docker.NotFound(err):
		s.envs.Seen(r.Context(), &env)
		s.writeError(w, http.StatusNotFound, "container not found")
	case docker.Conflict(err):
		s.envs.Seen(r.Context(), &env)
		s.writeError(w, http.StatusConflict, "the container cannot be recreated in its current state")
	default:
		s.dockerError(w, r, env, "container recreate", err)
	}
}
