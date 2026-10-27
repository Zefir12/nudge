package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// A user's containers are built by the server from a tenantSpec, a short list of fields,
// and never from Docker's own options: privileged mode, host binds, the Docker socket, the
// host's namespaces and devices can't be asked for, because nothing in the spec maps to
// them. buildTenant adds the hardening every such container gets. Every path that changes a
// user's container (create, recreate, image update, deploy webhooks) goes through it.

const (
	// tenantPidsLimit bounds the processes of a user's container (a fork bomb).
	tenantPidsLimit = 512
	// tenantLogMaxSize and tenantLogMaxFile bound the json-file logs of a user's container.
	tenantLogMaxSize = "10m"
	tenantLogMaxFile = "3"
	// minTenantMemory is the smallest memory limit Docker accepts.
	minTenantMemory = 6 << 20
	// maxTenantSeconds bounds a healthcheck's durations.
	maxTenantSeconds = 24 * 60 * 60
)

// tenantSpec is everything a user may set on their container.
type tenantSpec struct {
	Image string `json:"image"`
	// Name is without the user's prefix, which the server adds. On a recreate empty keeps
	// the container's name.
	Name string `json:"name"`
	// Command and Entrypoint replace the image's; empty keeps them.
	Command    []string `json:"command"`
	Entrypoint []string `json:"entrypoint"`
	Env        []string `json:"env"`
	// CPUsMilli (thousandths of a CPU) and MemoryBytes are required limits.
	CPUsMilli     int64         `json:"cpus_milli"`
	MemoryBytes   int64         `json:"memory_bytes"`
	Ports         []tenantPort  `json:"ports"`
	Mounts        []tenantMount `json:"mounts"`
	Networks      []string      `json:"networks"`
	RestartPolicy string        `json:"restart_policy"`
	// Healthcheck nil keeps the image's.
	Healthcheck *tenantHealth     `json:"healthcheck"`
	Labels      map[string]string `json:"labels"`
}

type tenantPort struct {
	// HostPort 0 picks a free one from the user's range.
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// tenantMount mounts one of the user's named volumes.
type tenantMount struct {
	Volume   string `json:"volume"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

// tenantHealth runs Command (exec form, no shell) in the container.
type tenantHealth struct {
	Command            []string `json:"command"`
	IntervalSeconds    int      `json:"interval_seconds"`
	TimeoutSeconds     int      `json:"timeout_seconds"`
	StartPeriodSeconds int      `json:"start_period_seconds"`
	Retries            int      `json:"retries"`
}

type createTenantRequest struct {
	tenantSpec
	Start bool `json:"start"`
}

// fieldsError is a request with invalid settings, answered as a fieldsResponse.
type fieldsError struct{ fields map[string]string }

func (e *fieldsError) Error() string { return "invalid container settings" }

// decodeStrict decodes a JSON body into v, refusing fields v doesn't have; on false it
// has answered.
func (s *server) decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	if err == nil {
		return true
	}
	// "json: unknown field \"privileged\"": the name is the client's own, quoted.
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok && len(field) <= 66 {
		s.writeError(w, http.StatusBadRequest, "unknown field "+field+": not a setting users may set")
		return false
	}
	s.writeError(w, http.StatusBadRequest, "invalid JSON body: "+jsonErrorKind(err))
	return false
}

// validate returns a message per invalid field, keyed like the JSON (ports.0.host_port).
// Whether the volumes, networks and image are the user's to use is checked against the
// daemon (tenantRefs, imageVolumes); the allowance by fitAllowance.
func (sp *tenantSpec) validate() map[string]string {
	errs := map[string]string{}
	sp.Image = strings.TrimSpace(sp.Image)
	sp.Name = strings.TrimSpace(sp.Name)
	switch {
	case sp.Image == "":
		errs["image"] = "required"
	case digest.MatchString(sp.Image) || !validPullRef(sp.Image):
		errs["image"] = "must be a reference such as nginx:1.29"
	}
	if sp.Name != "" && !objectID.MatchString(sp.Name) {
		errs["name"] = "letters, digits, '_', '.' or '-', starting with a letter or digit"
	}
	for i, e := range sp.Env {
		if k, _, _ := strings.Cut(e, "="); strings.TrimSpace(k) == "" {
			errs[fmt.Sprintf("env.%d", i)] = "must be NAME=value"
		}
	}
	if sp.CPUsMilli <= 0 {
		errs["cpus_milli"] = "required: a CPU limit in thousandths of a CPU"
	}
	if sp.MemoryBytes < minTenantMemory {
		errs["memory_bytes"] = "required: a memory limit of 6 MiB or more"
	}
	published := map[string]bool{}
	for i, p := range sp.Ports {
		key := func(f string) string { return fmt.Sprintf("ports.%d.%s", i, f) }
		if p.ContainerPort < 1 || p.ContainerPort > 65535 {
			errs[key("container_port")] = "must be 1 to 65535"
		}
		if p.HostPort < 0 || p.HostPort > 65535 {
			errs[key("host_port")] = "must be 0 (a free port of yours) or a port of your range"
		}
		if !slices.Contains([]string{"", "tcp", "udp"}, p.Protocol) {
			errs[key("protocol")] = "must be tcp or udp"
		}
		if hp := fmt.Sprintf("%d/%s", p.HostPort, cmpProto(p.Protocol)); p.HostPort != 0 && published[hp] {
			errs[key("host_port")] = "published twice"
		} else {
			published[hp] = true
		}
	}
	targets := map[string]bool{}
	for i, m := range sp.Mounts {
		key := func(f string) string { return fmt.Sprintf("mounts.%d.%s", i, f) }
		if !objectID.MatchString(m.Volume) {
			errs[key("volume")] = "must be the name of one of your volumes"
		}
		target := path.Clean(m.Target)
		switch {
		case !path.IsAbs(m.Target) || target == "/":
			errs[key("target")] = "must be an absolute path in the container, not /"
		case targets[target]:
			errs[key("target")] = "mounted twice"
		}
		targets[target] = true
	}
	for i, n := range sp.Networks {
		if !objectID.MatchString(n) || slices.Index(sp.Networks, n) < i {
			errs[fmt.Sprintf("networks.%d", i)] = "must be one of your networks, once"
		}
	}
	if !slices.Contains(restartPolicies, sp.RestartPolicy) {
		errs["restart_policy"] = "must be no, always, unless-stopped or on-failure"
	}
	if h := sp.Healthcheck; h != nil {
		if len(h.Command) == 0 || h.Command[0] == "" {
			errs["healthcheck.command"] = "required: the command and its arguments"
		}
		for f, v := range map[string]int{"interval_seconds": h.IntervalSeconds, "timeout_seconds": h.TimeoutSeconds, "start_period_seconds": h.StartPeriodSeconds} {
			if v < 0 || v > maxTenantSeconds {
				errs["healthcheck."+f] = "must be 0 (Docker's default) to 86400"
			}
		}
		if h.Retries < 0 || h.Retries > 100 {
			errs["healthcheck.retries"] = "must be 0 (Docker's default) to 100"
		}
	}
	if hasEmptyKey(sp.Labels) {
		errs["labels"] = "label names must not be empty"
	} else if reservedLabel(sp.Labels) {
		errs["labels"] = "labels starting with " + reservedLabels + " are reserved for nudge"
	}
	return errs
}

// cmpProto is a port's protocol, tcp when empty.
func cmpProto(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}

// buildTenant turns a validated spec into Docker's settings for a container of the user
// with owner key ("" for an admin's). Everything not taken from the spec is fixed here.
func buildTenant(sp *tenantSpec, owner string) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	labels := maps.Clone(sp.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.DeleteFunc(labels, func(k, _ string) bool { return reservedKey(k) })
	if owner != "" {
		// Set last, so it wins over the spec; an image's own value can't override a
		// container's label (and an image that sets one is refused, refuseOwnerImage).
		labels[ownerLabel] = owner
	}
	cfg := &container.Config{
		Image: sp.Image, Cmd: slices.Clone(sp.Command), Entrypoint: slices.Clone(sp.Entrypoint),
		Env: slices.Clone(sp.Env), Labels: labels,
	}
	if h := sp.Healthcheck; h != nil {
		cfg.Healthcheck = &container.HealthConfig{
			Test:     append([]string{"CMD"}, h.Command...),
			Interval: seconds(h.IntervalSeconds), Timeout: seconds(h.TimeoutSeconds),
			StartPeriod: seconds(h.StartPeriodSeconds), Retries: h.Retries,
		}
	}
	initProcess, pids := true, int64(tenantPidsLimit)
	hc := &container.HostConfig{
		// Docker's default capabilities without NET_RAW (raw sockets: spoofing on the
		// bridge), none added; no setuid gains; default seccomp and AppArmor profiles.
		CapDrop:     []string{"NET_RAW"},
		SecurityOpt: []string{"no-new-privileges:true"},
		Init:        &initProcess,
		IpcMode:     container.IPCModePrivate,
		LogConfig: container.LogConfig{Type: "json-file", Config: map[string]string{
			"max-size": tenantLogMaxSize, "max-file": tenantLogMaxFile,
		}},
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(sp.RestartPolicy)},
		Resources: container.Resources{
			NanoCPUs: sp.CPUsMilli * 1_000_000, Memory: sp.MemoryBytes,
			// No swap beyond the memory limit.
			MemorySwap: sp.MemoryBytes, PidsLimit: &pids,
		},
		PortBindings: network.PortMap{},
	}
	for _, p := range sp.Ports {
		// validate checked the range.
		port, _ := network.PortFrom(uint16(p.ContainerPort), network.IPProtocol(cmpProto(p.Protocol))) // #nosec G115 -- 1 to 65535
		b := network.PortBinding{}
		if p.HostPort != 0 {
			b.HostPort = strconv.Itoa(p.HostPort)
		}
		hc.PortBindings[port] = append(hc.PortBindings[port], b)
		if cfg.ExposedPorts == nil {
			cfg.ExposedPorts = network.PortSet{}
		}
		cfg.ExposedPorts[port] = struct{}{}
	}
	for _, m := range sp.Mounts {
		hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeVolume, Source: m.Volume, Target: path.Clean(m.Target), ReadOnly: m.ReadOnly})
	}
	nc := &network.NetworkingConfig{}
	// Without networks (an admin's): Docker's default bridge.
	if len(sp.Networks) > 0 {
		hc.NetworkMode = container.NetworkMode(sp.Networks[0])
		nc.EndpointsConfig = map[string]*network.EndpointSettings{}
		for _, n := range sp.Networks {
			nc.EndpointsConfig[n] = &network.EndpointSettings{}
		}
	}
	return cfg, hc, nc
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

// toTenantSpec reads a container's settings in the spec's shape: what the user set, without
// what its image (img, nil if gone) sets. prefix is stripped from its name; a name without
// it reads as "" (keep the name).
func toTenantSpec(ct container.InspectResponse, img *dockerspec.DockerOCIImageConfig, prefix string) tenantSpec {
	sp := tenantSpec{
		Command: []string{}, Entrypoint: []string{}, Env: []string{}, Ports: []tenantPort{},
		Mounts: []tenantMount{}, Networks: []string{}, Labels: map[string]string{},
	}
	if short, ok := strings.CutPrefix(strings.TrimPrefix(ct.Name, "/"), prefix); ok {
		sp.Name = short
	}
	if ct.Config != nil {
		cfg := *ct.Config
		dropImageDefaults(&cfg, img)
		sp.Image, sp.Command, sp.Entrypoint, sp.Env = cfg.Image, nonNil(cfg.Cmd), nonNil(cfg.Entrypoint), nonNil(cfg.Env)
		sp.Labels = nonNilMap(maps.Clone(cfg.Labels))
		maps.DeleteFunc(sp.Labels, func(k, _ string) bool { return reservedKey(k) })
		if h := cfg.Healthcheck; h != nil && len(h.Test) > 1 && h.Test[0] == "CMD" {
			sp.Healthcheck = &tenantHealth{
				Command: h.Test[1:], IntervalSeconds: int(h.Interval / time.Second), TimeoutSeconds: int(h.Timeout / time.Second),
				StartPeriodSeconds: int(h.StartPeriod / time.Second), Retries: h.Retries,
			}
		}
	}
	if hc := ct.HostConfig; hc != nil {
		sp.CPUsMilli, sp.MemoryBytes, sp.RestartPolicy = cpuLimit(hc), hc.Memory, string(hc.RestartPolicy.Name)
		for _, p := range toSpec(container.InspectResponse{HostConfig: hc}).Ports {
			sp.Ports = append(sp.Ports, tenantPort{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol})
		}
	}
	for _, m := range ct.Mounts {
		if m.Type == mount.TypeVolume {
			sp.Mounts = append(sp.Mounts, tenantMount{Volume: m.Name, Target: m.Destination, ReadOnly: !m.RW})
		}
	}
	if ns := ct.NetworkSettings; ns != nil {
		sp.Networks = sortedKeys(ns.Networks)
		// The network mode is the first: a recreate keeps it.
		if mode := ct.HostConfig; mode != nil {
			if i := slices.Index(sp.Networks, string(mode.NetworkMode)); i > 0 {
				sp.Networks = slices.Insert(slices.Delete(sp.Networks, i, i+1), 0, string(mode.NetworkMode))
			}
		}
	}
	return sp
}

// tenantOwner is who a spec is built for: the owner key ("" for an admin's container), the
// user's id and the prefix of their objects' names.
type tenantOwner struct {
	key    string
	id     int64
	prefix string
}

// tenantOwnerOf returns the owner with key ("" for the admins').
func (s *server) tenantOwnerOf(ctx context.Context, key string) (tenantOwner, error) {
	if key == "" {
		return tenantOwner{}, nil
	}
	id, err := s.auth.OwnerID(ctx, key)
	if err != nil {
		return tenantOwner{}, &internalFailure{msg: "owner", err: err}
	}
	return tenantOwner{key: key, id: id, prefix: userPrefix(id)}, nil
}

// resolve completes a validated spec for its owner and checks it against the daemon: the
// container's full name (oldName when the spec names none), the user's default network
// when the spec names none, and volumes and networks the owner's own. A refusal is a
// *fieldsError.
func (o tenantOwner) resolve(ctx context.Context, c docker.Client, sp *tenantSpec, oldName string) (string, error) {
	errs := map[string]string{}
	name := oldName
	switch {
	case sp.Name != "" && o.key != "":
		var msg string
		if name, msg = userName(o.id, sp.Name); msg != "" {
			errs["name"] = msg
		}
	case sp.Name != "":
		name = sp.Name
	case oldName == "":
		errs["name"] = "required"
	}
	if len(sp.Networks) == 0 && o.key != "" {
		sp.Networks = []string{defaultNetwork(o.id)}
	}
	if o.key != "" {
		for i, m := range sp.Mounts {
			res, err := c.VolumeInspect(ctx, m.Volume, client.VolumeInspectOptions{})
			if err != nil && !docker.NotFound(err) {
				return "", fmt.Errorf("inspect volume: %w", err)
			}
			if err != nil || res.Volume.Labels[ownerLabel] != o.key {
				errs[fmt.Sprintf("mounts.%d.volume", i)] = "not one of your volumes"
			}
		}
		for i, n := range sp.Networks {
			res, err := c.NetworkInspect(ctx, n, client.NetworkInspectOptions{})
			if err != nil && !docker.NotFound(err) {
				return "", fmt.Errorf("inspect network: %w", err)
			}
			// By name only: an ID (prefix) could name someone else's network.
			if err != nil || res.Network.Labels[ownerLabel] != o.key || res.Network.Name != n {
				errs[fmt.Sprintf("networks.%d", i)] = "not one of your networks"
			}
		}
	}
	if len(errs) > 0 {
		return "", &fieldsError{fields: errs}
	}
	return name, nil
}

// imageVolumes refuses an image with VOLUME paths the spec mounts nothing at. A missing
// image is left to the create to report.
func imageVolumes(ctx context.Context, c docker.Client, sp *tenantSpec) error {
	img, err := c.ImageInspect(ctx, sp.Image)
	if docker.NotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect image: %w", err)
	}
	if img.Config != nil {
		if msg := uncoveredVolumes(img.Config.Volumes, sp.Mounts); msg != "" {
			return &fieldsError{fields: map[string]string{"mounts": msg}}
		}
	}
	return nil
}

// uncoveredVolumes names the image's VOLUME paths no mount covers, or "". Docker would
// create an anonymous volume for each, which nobody owns and nothing counts.
func uncoveredVolumes(volumes map[string]struct{}, mounts []tenantMount) string {
	var missing []string
	for v := range volumes {
		if !slices.ContainsFunc(mounts, func(m tenantMount) bool { return path.Clean(m.Target) == path.Clean(v) }) {
			missing = append(missing, path.Clean(v))
		}
	}
	if len(missing) == 0 {
		return ""
	}
	slices.Sort(missing)
	return "the image keeps data in " + strings.Join(missing, ", ") + ": mount one of your volumes there"
}

func (s *server) writeFields(w http.ResponseWriter, errs map[string]string) {
	s.writeJSON(w, http.StatusBadRequest, fieldsResponse{Error: "invalid container settings", Fields: errs})
}

// createTenantContainer creates a container from a tenantSpec ({..., "start": true}) and
// answers 201. The image is always pulled, with the user's allowance and without the
// admins' registry credentials, so a user can't run a private image an admin pulled.
func (s *server) createTenantContainer(w http.ResponseWriter, r *http.Request) {
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	var req createTenantRequest
	if !s.decodeStrict(w, r, &req) {
		return
	}
	if errs := req.validate(); len(errs) > 0 {
		s.writeFields(w, errs)
		return
	}
	auditTarget(r, req.Name)
	// The pull has its own timeout; the create goes on if the client goes away.
	resp, err := s.createTenant(context.WithoutCancel(r.Context()), env.ID, c, &req)
	if err != nil {
		s.replaceError(w, r, env, err)
		return
	}
	s.envs.Seen(r.Context(), &env)
	s.writeJSON(w, http.StatusCreated, resp)
}

func (s *server) createTenant(ctx context.Context, env int64, c docker.Client, req *createTenantRequest) (createContainerResponse, error) {
	sc, err := s.scopeOf(ctx)
	if err != nil {
		return createContainerResponse{}, err
	}
	owner, err := s.tenantOwnerOf(ctx, sc.key)
	if err != nil {
		return createContainerResponse{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	name, err := owner.resolve(rctx, c, &req.tenantSpec, "")
	cancel()
	if err != nil {
		return createContainerResponse{}, err
	}
	if err := s.pullOwned(ctx, env, c, req.Image, owner.key); err != nil {
		return createContainerResponse{}, err
	}
	ctx, cancel = context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := imageVolumes(ctx, c, &req.tenantSpec); err != nil {
		return createContainerResponse{}, err
	}
	cfg, hc, nc := buildTenant(&req.tenantSpec, owner.key)
	unlock, err := s.fitAllowance(ctx, env, c, owner.key, cfg, hc, "", true)
	if err != nil {
		return createContainerResponse{}, err
	}
	defer unlock()
	res, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: hc, NetworkingConfig: nc})
	if err != nil {
		return createContainerResponse{}, refusal(err, "created", req.Env)
	}
	if req.Start {
		if _, err := c.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
			s.cleanUp(ctx, c, res.ID)
			return createContainerResponse{}, refusal(err, "started (it was removed again)", req.Env)
		}
	}
	return createContainerResponse{ID: res.ID, Warnings: nonNil(res.Warnings)}, nil
}

// getTenantSpec answers a container's settings as a tenantSpec, for the user's form.
func (s *server) getTenantSpec(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	s.dockerCall(w, r, "container", func(ctx context.Context, c docker.Client) (any, error) {
		ct, _, err := s.ownedContainer(ctx, c, id)
		if err != nil {
			return nil, err
		}
		img, err := imageConfig(ctx, c, ct.Image)
		if err != nil {
			return nil, err
		}
		var imageLabels map[string]string
		if img != nil {
			imageLabels = img.Labels
		}
		owner, err := s.tenantOwnerOf(ctx, containerOwner(ct, imageLabels))
		if err != nil {
			return nil, err
		}
		return toTenantSpec(ct, img, owner.prefix), nil
	})
}

// recreateTenantContainer replaces a container with one built from the tenantSpec in the
// body, after pulling its image (as createTenantContainer does).
func (s *server) recreateTenantContainer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	// Ownership first: someone else's container is 404 whatever the body.
	rp, ok := s.beginReplace(w, r, id)
	if !ok {
		return
	}
	defer rp.done()
	var sp tenantSpec
	if !s.decodeStrict(w, r, &sp) {
		return
	}
	if errs := sp.validate(); len(errs) > 0 {
		s.writeFields(w, errs)
		return
	}
	rp.tenant = &sp
	if !s.pullForReplace(w, r, rp, sp.Image) {
		return
	}
	newID, ok := s.finishReplace(w, r, rp, nil)
	if !ok {
		return
	}
	s.log.Info("container recreate", "env", rp.env.ID, "container", rp.old.ID, "new", newID, "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, createContainerResponse{ID: newID, Warnings: []string{}})
}

// errAdminSpec refuses an admin's containerSpec on a user's container: it could set what
// the tenant builder never does (binds, host networks).
var errAdminSpec = &statusError{code: http.StatusConflict, msg: "this container is a user's: change it with its user settings (user-containers), not the admin form"}

// tenantSettings builds the settings of a user's container being replaced: from the spec
// asked for (rp.tenant) or, for an image update or a recreate without one, from the old
// container's own settings read back as a spec. It returns the settings and the name.
func (s *server) tenantSettings(ctx context.Context, rp *replacement) (*container.Config, *container.HostConfig, *network.NetworkingConfig, string, error) {
	owner, err := s.tenantOwnerOf(ctx, rp.owner)
	if err != nil {
		return nil, nil, nil, "", err
	}
	sp := rp.tenant
	if sp == nil {
		read := toTenantSpec(rp.old, rp.oldImage.Config, owner.prefix)
		sp = &read
		if errs := sp.validate(); len(errs) > 0 {
			return nil, nil, nil, "", &fieldsError{fields: errs}
		}
	}
	name, err := owner.resolve(ctx, rp.c, sp, strings.TrimPrefix(rp.old.Name, "/"))
	if err == nil {
		err = imageVolumes(ctx, rp.c, sp)
	}
	if err != nil {
		return nil, nil, nil, "", err
	}
	cfg, hc, nc := buildTenant(sp, owner.key)
	return cfg, hc, nc, name, nil
}
