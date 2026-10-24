package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// containerStates are the values of a container's state and of the ?state= filter.
var containerStates = map[string]bool{
	"created": true, "running": true, "paused": true, "restarting": true,
	"removing": true, "exited": true, "dead": true,
}

type containerSummaryResponse struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	ImageID   string            `json:"image_id"`
	Command   string            `json:"command"`
	CreatedAt *time.Time        `json:"created_at"`
	State     string            `json:"state"`
	Status    string            `json:"status"`
	Health    string            `json:"health"`
	Ports     []portResponse    `json:"ports"`
	Labels    map[string]string `json:"labels"`
	Networks  []string          `json:"networks"`
	Mounts    []mountResponse   `json:"mounts"`
}

type portResponse struct {
	PrivatePort uint16 `json:"private_port"`
	Protocol    string `json:"protocol"`
	// HostIP and PublicPort are empty and 0 when the port is not published.
	HostIP     string `json:"host_ip"`
	PublicPort uint16 `json:"public_port"`
}

type mountResponse struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadWrite   bool   `json:"rw"`
}

type containerDetailResponse struct {
	ID            string                     `json:"id"`
	Name          string                     `json:"name"`
	Image         string                     `json:"image"`
	ImageID       string                     `json:"image_id"`
	CreatedAt     *time.Time                 `json:"created_at"`
	State         containerStateResponse     `json:"state"`
	RestartCount  int                        `json:"restart_count"`
	Path          string                     `json:"path"`
	Args          []string                   `json:"args"`
	Config        containerConfigResponse    `json:"config"`
	RestartPolicy restartPolicyResponse      `json:"restart_policy"`
	NetworkMode   string                     `json:"network_mode"`
	Privileged    bool                       `json:"privileged"`
	Ports         []portResponse             `json:"ports"`
	Mounts        []mountResponse            `json:"mounts"`
	Networks      []containerNetworkResponse `json:"networks"`
}

type containerStateResponse struct {
	Status     string     `json:"status"`
	Running    bool       `json:"running"`
	Paused     bool       `json:"paused"`
	Restarting bool       `json:"restarting"`
	OOMKilled  bool       `json:"oom_killed"`
	Dead       bool       `json:"dead"`
	Pid        int        `json:"pid"`
	ExitCode   int        `json:"exit_code"`
	Error      string     `json:"error"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Health     string     `json:"health"`
}

type containerConfigResponse struct {
	Hostname     string            `json:"hostname"`
	User         string            `json:"user"`
	Env          []string          `json:"env"`
	Cmd          []string          `json:"cmd"`
	Entrypoint   []string          `json:"entrypoint"`
	WorkingDir   string            `json:"working_dir"`
	ExposedPorts []string          `json:"exposed_ports"`
	Labels       map[string]string `json:"labels"`
	Tty          bool              `json:"tty"`
}

type restartPolicyResponse struct {
	Name              string `json:"name"`
	MaximumRetryCount int    `json:"maximum_retry_count"`
}

type containerNetworkResponse struct {
	Name       string   `json:"name"`
	NetworkID  string   `json:"network_id"`
	IPAddress  string   `json:"ip_address"`
	Gateway    string   `json:"gateway"`
	MacAddress string   `json:"mac_address"`
	Aliases    []string `json:"aliases"`
}

func (s *server) listContainers(w http.ResponseWriter, r *http.Request) {
	opts := client.ContainerListOptions{All: true}
	if state := r.URL.Query().Get("state"); state != "" {
		if !containerStates[state] {
			s.writeError(w, http.StatusBadRequest, "unknown state "+strconv.Quote(state))
			return
		}
		opts.Filters = make(client.Filters).Add("status", state)
	}
	s.dockerCall(w, r, "list containers", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.ContainerList(ctx, opts)
		if err != nil {
			return nil, err
		}
		resp := make([]containerSummaryResponse, len(res.Items))
		for i, c := range res.Items {
			resp[i] = toContainerSummary(c)
		}
		return resp, nil
	})
}

func (s *server) getContainer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	s.dockerCall(w, r, "container", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		return toContainerDetail(res.Container), nil
	})
}

func toContainerSummary(c container.Summary) containerSummaryResponse {
	r := containerSummaryResponse{
		ID: c.ID, Image: c.Image, ImageID: c.ImageID, Command: c.Command,
		CreatedAt: unixTime(c.Created), State: string(c.State), Status: c.Status,
		Labels: nonNilMap(c.Labels), Ports: []portResponse{}, Networks: []string{},
		Mounts: toMounts(c.Mounts),
	}
	if len(c.Names) > 0 {
		r.Name = strings.TrimPrefix(c.Names[0], "/")
	}
	if c.Health != nil {
		r.Health = string(c.Health.Status)
	}
	for _, p := range c.Ports {
		pr := portResponse{PrivatePort: p.PrivatePort, Protocol: p.Type, PublicPort: p.PublicPort}
		if p.IP.IsValid() {
			pr.HostIP = p.IP.String()
		}
		r.Ports = append(r.Ports, pr)
	}
	if c.NetworkSettings != nil {
		r.Networks = sortedKeys(c.NetworkSettings.Networks)
	}
	return r
}

func toContainerDetail(c container.InspectResponse) containerDetailResponse {
	r := containerDetailResponse{
		ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), ImageID: c.Image,
		CreatedAt: parseTime(c.Created), RestartCount: c.RestartCount, Path: c.Path,
		Args: nonNil(c.Args), Ports: []portResponse{}, Mounts: toMounts(c.Mounts),
		Networks: []containerNetworkResponse{},
		Config:   containerConfigResponse{Env: []string{}, Cmd: []string{}, Entrypoint: []string{}, ExposedPorts: []string{}, Labels: map[string]string{}},
	}
	if st := c.State; st != nil {
		r.State = containerStateResponse{
			Status: string(st.Status), Running: st.Running, Paused: st.Paused,
			Restarting: st.Restarting, OOMKilled: st.OOMKilled, Dead: st.Dead, Pid: st.Pid,
			ExitCode: st.ExitCode, Error: st.Error, StartedAt: parseTime(st.StartedAt),
			FinishedAt: parseTime(st.FinishedAt),
		}
		if st.Health != nil {
			r.State.Health = string(st.Health.Status)
		}
	}
	if cfg := c.Config; cfg != nil {
		r.Image = cfg.Image
		r.Config = containerConfigResponse{
			Hostname: cfg.Hostname, User: cfg.User, Env: nonNil(cfg.Env), Cmd: nonNil(cfg.Cmd),
			Entrypoint: nonNil(cfg.Entrypoint), WorkingDir: cfg.WorkingDir,
			ExposedPorts: portNames(cfg.ExposedPorts), Labels: nonNilMap(cfg.Labels), Tty: cfg.Tty,
		}
	}
	if hc := c.HostConfig; hc != nil {
		r.RestartPolicy = restartPolicyResponse{Name: string(hc.RestartPolicy.Name), MaximumRetryCount: hc.RestartPolicy.MaximumRetryCount}
		r.NetworkMode = string(hc.NetworkMode)
		r.Privileged = hc.Privileged
	}
	if ns := c.NetworkSettings; ns != nil {
		r.Ports = toPortBindings(ns.Ports)
		for _, name := range sortedKeys(ns.Networks) {
			ep := ns.Networks[name]
			if ep == nil {
				continue
			}
			n := containerNetworkResponse{Name: name, NetworkID: ep.NetworkID, MacAddress: ep.MacAddress.String(), Aliases: nonNil(ep.Aliases)}
			if ep.IPAddress.IsValid() {
				n.IPAddress = ep.IPAddress.String()
			}
			if ep.Gateway.IsValid() {
				n.Gateway = ep.Gateway.String()
			}
			r.Networks = append(r.Networks, n)
		}
	}
	return r
}

func toMounts(mounts []container.MountPoint) []mountResponse {
	r := make([]mountResponse, len(mounts))
	for i, m := range mounts {
		r[i] = mountResponse{Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination, ReadWrite: m.RW}
	}
	return r
}

// toPortBindings flattens a port map into one entry per binding, plus one for each
// exposed port that is not published, ordered by port.
func toPortBindings(ports network.PortMap) []portResponse {
	r := []portResponse{}
	for port, bindings := range ports {
		if len(bindings) == 0 {
			r = append(r, portResponse{PrivatePort: port.Num(), Protocol: string(port.Proto())})
		}
		for _, b := range bindings {
			pr := portResponse{PrivatePort: port.Num(), Protocol: string(port.Proto())}
			if b.HostIP.IsValid() {
				pr.HostIP = b.HostIP.String()
			}
			if n, err := strconv.ParseUint(b.HostPort, 10, 16); err == nil {
				pr.PublicPort = uint16(n)
			}
			r = append(r, pr)
		}
	}
	sort.Slice(r, func(i, j int) bool {
		a, b := r[i], r[j]
		if a.PrivatePort != b.PrivatePort {
			return a.PrivatePort < b.PrivatePort
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		if a.HostIP != b.HostIP {
			return a.HostIP < b.HostIP
		}
		return a.PublicPort < b.PublicPort
	})
	return r
}

// portNames lists exposed ports as "80/tcp", in order.
func portNames(ports network.PortSet) []string {
	names := make([]string, 0, len(ports))
	for p := range ports {
		names = append(names, p.String())
	}
	sort.Strings(names)
	return names
}
