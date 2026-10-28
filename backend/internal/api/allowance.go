package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/allowances"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// allowanceJSON is a user's allowance in one environment.
type allowanceJSON struct {
	CPUsMilli     int64      `json:"cpus_milli"`
	MemoryBytes   int64      `json:"memory_bytes"`
	DiskBytes     int64      `json:"disk_bytes"`
	MaxContainers int64      `json:"max_containers"`
	PortMin       int        `json:"port_min"`
	PortMax       int        `json:"port_max"`
	Images        []string   `json:"images"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

func toAllowanceJSON(a allowances.Allowance) *allowanceJSON {
	at := a.UpdatedAt.UTC()
	return &allowanceJSON{
		CPUsMilli: a.CPUsMilli, MemoryBytes: a.MemoryBytes, DiskBytes: a.DiskBytes, MaxContainers: a.MaxContainers,
		PortMin: a.PortMin, PortMax: a.PortMax, Images: nonNil(a.Images), UpdatedAt: &at,
	}
}

type allowanceUsageJSON struct {
	CPUsMilli   int64 `json:"cpus_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
	DiskBytes   int64 `json:"disk_bytes"`
	Containers  int64 `json:"containers"`
}

func toAllowanceUsageJSON(u allowances.Usage) allowanceUsageJSON {
	return allowanceUsageJSON{CPUsMilli: u.CPUsMilli, MemoryBytes: u.MemoryBytes, DiskBytes: u.DiskBytes, Containers: u.Containers}
}

// allowanceResponse is a user's allowance (null without one), what their containers,
// volumes and images use, and what is left.
type allowanceResponse struct {
	User   int64               `json:"user"`
	Limits *allowanceJSON      `json:"limits"`
	Used   allowanceUsageJSON  `json:"used"`
	Left   *allowanceUsageJSON `json:"left"`
}

// allowanceUser returns the user whose allowance a request is about: a user's own, the
// ?user= one for an admin (who has none). On false it has answered.
func (s *server) allowanceUser(w http.ResponseWriter, r *http.Request) (auth.Account, string, bool) {
	u, _ := userFrom(r.Context())
	q := r.URL.Query().Get("user")
	if !isAdmin(u) {
		if q != "" && q != strconv.FormatInt(u.ID, 10) {
			s.writeError(w, http.StatusForbidden, "only admins can see another user's allowance")
			return auth.Account{}, "", false
		}
		q = strconv.FormatInt(u.ID, 10)
	}
	id, err := strconv.ParseInt(q, 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, http.StatusBadRequest, "user must be a user id")
		return auth.Account{}, "", false
	}
	a, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		s.userError(w, "user", err)
		return auth.Account{}, "", false
	}
	if a.Role != auth.RoleUser {
		s.writeError(w, http.StatusBadRequest, "admins have no allowance")
		return auth.Account{}, "", false
	}
	key, err := s.auth.OwnerKey(r.Context(), a.ID)
	if err != nil {
		s.internalError(w, "owner key", err)
		return auth.Account{}, "", false
	}
	auditTarget(r, a.Username)
	return a, key, true
}

func (s *server) getAllowance(w http.ResponseWriter, r *http.Request) {
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	u, key, ok := s.allowanceUser(w, r)
	if !ok {
		return
	}
	a, err := s.allowances.Get(r.Context(), u.ID, env.ID)
	found := err == nil
	if err != nil && !errors.Is(err, allowances.ErrNotFound) {
		s.internalError(w, "get allowance", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dockerTimeout)
	defer cancel()
	used, _, err := s.allowanceUsage(ctx, env.ID, c, u.ID, key, a, "")
	if err != nil {
		s.replaceError(w, r, env, err)
		return
	}
	s.envs.Seen(r.Context(), &env)
	resp := allowanceResponse{User: u.ID, Used: toAllowanceUsageJSON(used)}
	if found {
		resp.Limits = toAllowanceJSON(a)
		left := toAllowanceUsageJSON(allowances.Left(a, used))
		resp.Left = &left
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) setAllowance(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	u, _, ok := s.allowanceUser(w, r)
	if !ok {
		return
	}
	var req allowanceJSON
	if !s.decodeJSON(w, r, &req) {
		return
	}
	a := allowances.Allowance{
		CPUsMilli: req.CPUsMilli, MemoryBytes: req.MemoryBytes, DiskBytes: req.DiskBytes, MaxContainers: req.MaxContainers,
		PortMin: req.PortMin, PortMax: req.PortMax, Images: req.Images,
	}
	if errs := a.Validate(); errs != nil {
		s.writeJSON(w, http.StatusBadRequest, fieldsResponse{Error: "invalid allowance", Fields: errs})
		return
	}
	a, err := s.allowances.Set(r.Context(), u.ID, env.ID, a)
	if err != nil {
		s.internalError(w, "set allowance", err)
		return
	}
	s.log.Info("allowance set", "username", u.Username, "env", env.ID, "by", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, toAllowanceJSON(a))
}

func (s *server) deleteAllowance(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	u, _, ok := s.allowanceUser(w, r)
	if !ok {
		return
	}
	err := s.allowances.Delete(r.Context(), u.ID, env.ID)
	if errors.Is(err, allowances.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "the user has no allowance in this environment")
		return
	}
	if err != nil {
		s.internalError(w, "delete allowance", err)
		return
	}
	s.log.Info("allowance removed", "username", u.Username, "env", env.ID, "by", s.username(r), "ip", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// allowanceUsage adds up what the containers, volumes and images of the user with owner
// key take in env, leaving out container skip (one being replaced or started), and
// returns the host ports taken by every other container.
func (s *server) allowanceUsage(ctx context.Context, env int64, c docker.Client, user int64, key string, a allowances.Allowance, skip string) (allowances.Usage, map[int]bool, error) {
	sc := scope{key: key}
	taken := map[int]bool{}
	all, err := c.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return allowances.Usage{}, nil, fmt.Errorf("list containers: %w", err)
	}
	for _, ct := range all.Items {
		if ct.ID == skip {
			continue
		}
		for _, p := range ct.Ports {
			if p.PublicPort != 0 {
				taken[int(p.PublicPort)] = true
			}
		}
	}
	// The writable layers' sizes: Docker computes them for these containers only.
	res, err := c.ContainerList(ctx, client.ContainerListOptions{All: true, Size: true, Filters: sc.filters(client.Filters{})})
	if err != nil {
		return allowances.Usage{}, nil, fmt.Errorf("list containers: %w", err)
	}
	owned, err := sc.ownedContainers(ctx, c, res.Items)
	if err != nil {
		return allowances.Usage{}, nil, err
	}
	var (
		limits []allowances.Container
		disk   int64
		vols   = map[string]bool{}
	)
	for _, ct := range owned {
		if ct.ID == skip {
			continue
		}
		ins, err := c.ContainerInspect(ctx, ct.ID, client.ContainerInspectOptions{})
		if docker.NotFound(err) {
			continue
		}
		if err != nil {
			return allowances.Usage{}, nil, fmt.Errorf("inspect container: %w", err)
		}
		if hc := ins.Container.HostConfig; hc != nil {
			limits = append(limits, allowances.Container{CPUsMilli: cpuLimit(hc), MemoryBytes: hc.Memory})
			// A stopped container's ports are free now but taken again when it starts.
			for _, p := range hostPorts(hc.PortBindings) {
				taken[p] = true
			}
		} else {
			limits = append(limits, allowances.Container{})
		}
		disk += ct.SizeRw
		// Named and anonymous (an image's VOLUME) volumes alike.
		for _, m := range ins.Container.Mounts {
			if m.Type == mount.TypeVolume && m.Name != "" {
				vols[m.Name] = true
			}
		}
	}
	vl, err := c.VolumeList(ctx, client.VolumeListOptions{Filters: sc.filters(client.Filters{})})
	if err != nil {
		return allowances.Usage{}, nil, fmt.Errorf("list volumes: %w", err)
	}
	for _, v := range vl.Items {
		if sc.owns(v.Labels) {
			vols[v.Name] = true
		}
	}
	if s.usage != nil && len(vols) > 0 {
		sizes, err := s.usage.Volumes(ctx, env)
		if err != nil {
			return allowances.Usage{}, nil, &internalFailure{msg: "volume usage", err: err}
		}
		for name := range vols {
			if v, ok := sizes[name]; ok && v.Size != nil {
				disk += *v.Size
			}
		}
	}
	pulls, err := s.allowances.Pulls(ctx, user, env)
	if err != nil {
		return allowances.Usage{}, nil, &internalFailure{msg: "image pulls", err: err}
	}
	for _, id := range pulls {
		img, err := c.ImageInspect(ctx, id)
		if docker.NotFound(err) {
			// Removed since: it takes no space any more.
			if err := s.allowances.ForgetPull(ctx, user, env, id); err != nil {
				return allowances.Usage{}, nil, &internalFailure{msg: "image pulls", err: err}
			}
			continue
		}
		if err != nil {
			return allowances.Usage{}, nil, fmt.Errorf("inspect image: %w", err)
		}
		disk += img.Size
	}
	return allowances.Tally(a, limits, disk), taken, nil
}

// cpuLimit is a container's CPU limit in thousandths of a CPU, 0 if it has none.
func cpuLimit(hc *container.HostConfig) int64 {
	if hc.NanoCPUs > 0 {
		return max(hc.NanoCPUs/1_000_000, 1)
	}
	if hc.CPUQuota > 0 {
		period := hc.CPUPeriod
		if period <= 0 {
			period = 100_000 // the kernel's default
		}
		return max(hc.CPUQuota*1000/period, 1)
	}
	return 0
}

// hostPorts lists the host ports bindings publish; a port range counts every port in it
// and a binding without a port (any free one) counts as 0.
func hostPorts(bindings network.PortMap) []int {
	var out []int
	for _, bs := range bindings {
		for _, b := range bs {
			lo, hi, ok := strings.Cut(b.HostPort, "-")
			first, err1 := strconv.Atoi(lo)
			last, err2 := strconv.Atoi(hi)
			switch {
			case b.HostPort == "":
				out = append(out, 0)
			case !ok && err1 == nil:
				out = append(out, first)
			case ok && err1 == nil && err2 == nil && first <= last && last-first < 65536:
				for p := first; p <= last; p++ {
					out = append(out, p)
				}
			default:
				out = append(out, -1)
			}
		}
	}
	return out
}

// pickPorts gives each of hc's bindings without a host port the lowest free one in a's
// range. The bindings are copied, not changed in place.
func pickPorts(a allowances.Allowance, hc *container.HostConfig, taken map[int]bool) error {
	bindings := network.PortMap{}
	keys := slices.SortedFunc(maps.Keys(hc.PortBindings), func(x, y network.Port) int { return strings.Compare(x.String(), y.String()) })
	for _, k := range keys {
		bs := slices.Clone(hc.PortBindings[k])
		for i, b := range bs {
			if b.HostPort != "" && b.HostPort != "0" {
				continue
			}
			p, ok := allowances.FreePort(a, taken)
			if !ok {
				return &statusError{code: http.StatusConflict, msg: "over your allowance: no free host port left in your ports"}
			}
			taken[p] = true
			bs[i].HostPort = strconv.Itoa(p)
		}
		bindings[k] = bs
	}
	hc.PortBindings = bindings
	return nil
}

// allowanceFailure turns a check's refusal into the answer.
func allowanceFailure(err error) error {
	var ex *allowances.ExceededError
	if errors.As(err, &ex) {
		return &statusError{code: http.StatusConflict, msg: "over your allowance: " + ex.Error()}
	}
	return err
}

// lockAllowance takes the lock of the user with owner key in env and returns their id
// and allowance. The caller unlocks.
func (s *server) lockAllowance(ctx context.Context, env int64, key string) (int64, allowances.Allowance, func(), error) {
	user, err := s.auth.OwnerID(ctx, key)
	if errors.Is(err, auth.ErrUserNotFound) {
		return 0, allowances.Allowance{}, nil, &statusError{code: http.StatusConflict, msg: "the container's owner no longer exists"}
	}
	if err != nil {
		return 0, allowances.Allowance{}, nil, &internalFailure{msg: "allowance", err: err}
	}
	unlock, err := s.allowances.Lock(ctx, user, env)
	if err != nil {
		return 0, allowances.Allowance{}, nil, err
	}
	a, err := s.allowances.Get(ctx, user, env)
	if err != nil {
		unlock()
		if errors.Is(err, allowances.ErrNotFound) {
			return 0, allowances.Allowance{}, nil, &statusError{code: http.StatusConflict, msg: "you have no allowance in this environment: ask an admin for one"}
		}
		return 0, allowances.Allowance{}, nil, &internalFailure{msg: "allowance", err: err}
	}
	return user, a, unlock, nil
}

// fitAllowance checks that a container of the user with owner key ("" is the admins',
// which has no allowance) fits their allowance in env: cfg and hc are its settings, skip
// the container it replaces or is (a start), create false for a start. Bindings without
// a host port get one from the user's range. It returns the unlock to call once the
// container is created or started, so the user's next check sees it.
func (s *server) fitAllowance(ctx context.Context, env int64, c docker.Client, key string, cfg *container.Config, hc *container.HostConfig, skip string, create bool) (func(), error) {
	if key == "" {
		return func() {}, nil
	}
	if create {
		// An image that sets the owner label makes its container the admins' (see
		// ownsContainer): a user's would escape every later check. Docker copies an
		// image's labels onto its containers, so denied labels are refused there too.
		if err := refuseImageLabels(ctx, c, cfg.Image, s.deniedLabels); err != nil {
			return nil, err
		}
	}
	user, a, unlock, err := s.lockAllowance(ctx, env, key)
	if err != nil {
		return nil, err
	}
	used, taken, err := s.allowanceUsage(ctx, env, c, user, key, a, skip)
	if err == nil && create {
		err = pickPorts(a, hc, taken)
	}
	if err == nil {
		err = allowanceFailure(allowances.Check(a, used, allowances.Request{
			Image: cfg.Image, CPUsMilli: cpuLimit(hc), MemoryBytes: hc.Memory, Ports: hostPorts(hc.PortBindings), Create: create,
		}))
	}
	if err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// refuseImageLabels refuses image for a user's container if it sets a label in nudge's
// namespace or one starting with a prefix of denied. A missing image is left to the
// create to report.
func refuseImageLabels(ctx context.Context, c docker.Client, image string, denied []string) error {
	img, err := c.ImageInspect(ctx, image)
	if docker.NotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect image: %w", err)
	}
	if img.Config != nil {
		for _, k := range slices.Sorted(maps.Keys(img.Config.Labels)) {
			if reservedKey(k) {
				return &statusError{code: http.StatusBadRequest, msg: "the image sets labels starting with " + reservedLabels + ", which are reserved for nudge"}
			}
			if p := deniedLabel(k, denied); p != "" {
				return &statusError{code: http.StatusBadRequest, msg: "the image sets labels starting with " + p + ", which are not allowed on this server"}
			}
		}
	}
	return nil
}

// fitStart checks that container id may start: a user's container must have the
// configured hosting (checkStartHosting), fit their allowance (if it was lowered since the
// container was created) and its ports their range.
func (s *server) fitStart(ctx context.Context, env int64, c docker.Client, id string) (func(), error) {
	ct, key, err := inspectOwner(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if key != "" && ct.HostConfig == nil {
		return nil, errors.New("inspect: incomplete container settings")
	}
	if err := s.checkStartHosting(ctx, c, key, ct.HostConfig); err != nil {
		return nil, err
	}
	return s.fitAllowance(ctx, env, c, key, ct.Config, ct.HostConfig, ct.ID, false)
}

// pathEnv is the request's {env}, which s.environment has checked.
func pathEnv(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("env"), 10, 64)
	return id
}

// containerOwner returns the owner key of container ct, "" for the admins': like
// ownsContainer, a container whose image sets the owner label is the admins'.
func containerOwner(ct container.InspectResponse, imageLabels map[string]string) string {
	if ct.Config == nil {
		return ""
	}
	if _, ok := imageLabels[ownerLabel]; ok {
		return ""
	}
	return ct.Config.Labels[ownerLabel]
}

// inspectOwner inspects container id and returns it and its owner key.
func inspectOwner(ctx context.Context, c docker.Client, id string) (container.InspectResponse, string, error) {
	res, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, "", err
	}
	ct := res.Container
	if ct.Config == nil || ct.Config.Labels[ownerLabel] == "" {
		return ct, "", nil
	}
	img, err := c.ImageInspect(ctx, ct.Image)
	var labels map[string]string
	switch {
	case docker.NotFound(err):
	case err != nil:
		return container.InspectResponse{}, "", fmt.Errorf("inspect image: %w", err)
	case img.Config != nil:
		labels = img.Config.Labels
	}
	return ct, containerOwner(ct, labels), nil
}

// pullOwned pulls ref for the user with owner key ("" pulls for an admin, unchecked, with
// the registry's stored credentials; a user's pull goes without any).
// A user's pull is refused when their disk allowance is used up; an image it brings counts
// toward it, and one that takes them over is removed again and the pull refused. The
// user's lock is held throughout, so their parallel pulls can't overshoot together.
func (s *server) pullOwned(ctx context.Context, env int64, c docker.Client, ref, key string) error {
	if key == "" {
		return s.pullWithAuth(ctx, c, ref)
	}
	user, a, unlock, err := s.lockAllowance(ctx, env, key)
	if err != nil {
		return err
	}
	defer unlock()
	used, _, err := s.allowanceUsage(ctx, env, c, user, key, a, "")
	if err != nil {
		return err
	}
	if err := allowanceFailure(allowances.CheckPull(a, used, ref)); err != nil {
		return err
	}
	// The images on the host before the pull: only one the pull brought is the user's
	// to count, or to remove again.
	list, err := c.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	present := map[string]bool{}
	for _, img := range list.Items {
		present[img.ID] = true
	}
	// Never with the admins' stored registry credentials: they would let a user run a
	// private image.
	pctx, cancel := context.WithTimeout(ctx, pullTimeout)
	err = pullAndWait(pctx, c, ref, client.ImagePullOptions{})
	cancel()
	if err != nil {
		return &pullFailure{err: err}
	}
	img, err := c.ImageInspect(ctx, ref)
	if err != nil {
		return &dockerFailure{msg: "pull", err: fmt.Errorf("inspect pulled image: %w", err)}
	}
	if present[img.ID] {
		return nil
	}
	if err := s.allowances.RecordPull(ctx, user, env, img.ID); err != nil {
		return &internalFailure{msg: "record pull", err: err}
	}
	used, _, err = s.allowanceUsage(ctx, env, c, user, key, a, "")
	if err == nil {
		err = allowanceFailure(allowances.CheckDisk(a, used))
		if err != nil {
			if _, rerr := c.ImageRemove(ctx, img.ID, client.ImageRemoveOptions{}); rerr != nil {
				s.log.Warn("remove image over the allowance", "env", env, "image", img.ID, "err", rerr)
				return err
			}
		}
	}
	if err != nil {
		if ferr := s.allowances.ForgetPull(ctx, user, env, img.ID); ferr != nil {
			return &internalFailure{msg: "image pulls", err: ferr}
		}
		return err
	}
	return nil
}

// dropPulledImage removes image, which a user's container ran before an update, if the
// user's pull brought it: it would count toward their disk until an admin pruned it. An
// image still in use or tagged elsewhere stays (Docker refuses).
func (s *server) dropPulledImage(ctx context.Context, env int64, c docker.Client, key, image string) {
	if key == "" {
		return
	}
	user, err := s.auth.OwnerID(ctx, key)
	if err != nil {
		return
	}
	pulls, err := s.allowances.Pulls(ctx, user, env)
	if err != nil || !slices.Contains(pulls, image) {
		return
	}
	if _, err := c.ImageRemove(ctx, image, client.ImageRemoveOptions{}); err != nil {
		s.log.Debug("old image kept after update", "env", env, "image", image, "err", err)
		return
	}
	if err := s.allowances.ForgetPull(ctx, user, env, image); err != nil {
		s.log.Warn("forget pull", "env", env, "image", image, "err", err)
	}
}
