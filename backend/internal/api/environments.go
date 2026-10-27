package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

const (
	// dockerTimeout bounds every Docker call a request makes.
	dockerTimeout = 10 * time.Second
	// maxConcurrentPings bounds the online checks one list request runs at once.
	maxConcurrentPings = 8
)

type environmentResponse struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Online     bool       `json:"online"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	// Agent is set for agent environments only.
	Agent *agentResponse `json:"agent,omitempty"`
}

type agentResponse struct {
	Enrolled bool   `json:"enrolled"`
	Version  string `json:"version"`
}

func toEnvironmentResponse(env environments.Environment, online bool) environmentResponse {
	r := environmentResponse{ID: env.ID, Name: env.Name, Kind: env.Kind, Online: online, CreatedAt: env.CreatedAt.UTC()}
	if env.Kind == environments.KindAgent {
		r.Agent = &agentResponse{Enrolled: env.Enrolled, Version: env.AgentVersion}
	}
	if env.LastSeenAt != nil {
		ls := env.LastSeenAt.UTC()
		r.LastSeenAt = &ls
	}
	return r
}

// environmentDetailResponse picks fields from Docker's info and version: the full
// objects carry proxy settings and registry configuration, which may hold credentials.
type environmentDetailResponse struct {
	environmentResponse
	Docker dockerResponse `json:"docker"`
}

type dockerResponse struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	APIVersion        string `json:"api_version"`
	MinAPIVersion     string `json:"min_api_version"`
	OS                string `json:"os"`
	OSType            string `json:"os_type"`
	KernelVersion     string `json:"kernel_version"`
	Architecture      string `json:"architecture"`
	NCPU              int    `json:"ncpu"`
	MemTotal          int64  `json:"mem_total"`
	Containers        int    `json:"containers"`
	ContainersRunning int    `json:"containers_running"`
	ContainersPaused  int    `json:"containers_paused"`
	ContainersStopped int    `json:"containers_stopped"`
	Images            int    `json:"images"`
	StorageDriver     string `json:"storage_driver"`
	DockerRootDir     string `json:"docker_root_dir"`
}

// listEnvironments answers every environment to an admin, to a user the ones they may use.
func (s *server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := s.envs.List(r.Context())
	if err != nil {
		s.internalError(w, "list environments", err)
		return
	}
	if u, ok := userFrom(r.Context()); ok && !isAdmin(u) {
		ids, err := s.envs.Access(r.Context(), u.ID)
		if err != nil {
			s.internalError(w, "list environments", err)
			return
		}
		envs = slices.DeleteFunc(envs, func(env environments.Environment) bool { return !slices.Contains(ids, env.ID) })
	}
	resp := make([]environmentResponse, len(envs))
	sem := make(chan struct{}, maxConcurrentPings)
	var wg sync.WaitGroup
	for i := range envs {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			online := s.envs.Online(r.Context(), &envs[i])
			resp[i] = toEnvironmentResponse(envs[i], online)
		})
	}
	wg.Wait()
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) getEnvironment(w http.ResponseWriter, r *http.Request) {
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dockerTimeout)
	defer cancel()
	info, err := c.Info(ctx, client.InfoOptions{})
	if err != nil {
		s.dockerError(w, r, env, "docker info", err)
		return
	}
	version, err := c.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		s.dockerError(w, r, env, "docker version", err)
		return
	}
	i := info.Info
	// A user learns about the host but counts only their own containers, and no images.
	sc, err := s.scopeOf(ctx)
	if err == nil && !sc.all {
		var list client.ContainerListResult
		if list, err = c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: sc.filters(nil)}); err == nil {
			list.Items, err = sc.ownedContainers(ctx, c, list.Items)
		}
		i.Containers, i.ContainersRunning, i.ContainersPaused, i.ContainersStopped, i.Images = len(list.Items), 0, 0, 0, 0
		for _, ct := range list.Items {
			switch ct.State {
			case container.StateRunning:
				i.ContainersRunning++
			case container.StatePaused:
				i.ContainersPaused++
			default:
				i.ContainersStopped++
			}
		}
	}
	if err != nil {
		s.dockerError(w, r, env, "docker info", err)
		return
	}
	if !sc.all {
		// Nor the host's name or Docker's paths.
		i.Name, i.DockerRootDir = "", ""
	}
	s.envs.Seen(r.Context(), &env)
	s.writeJSON(w, http.StatusOK, environmentDetailResponse{
		environmentResponse: toEnvironmentResponse(env, true),
		Docker: dockerResponse{
			Name:              i.Name,
			Version:           version.Version,
			APIVersion:        version.APIVersion,
			MinAPIVersion:     version.MinAPIVersion,
			OS:                i.OperatingSystem,
			OSType:            i.OSType,
			KernelVersion:     i.KernelVersion,
			Architecture:      i.Architecture,
			NCPU:              i.NCPU,
			MemTotal:          i.MemTotal,
			Containers:        i.Containers,
			ContainersRunning: i.ContainersRunning,
			ContainersPaused:  i.ContainersPaused,
			ContainersStopped: i.ContainersStopped,
			Images:            i.Images,
			StorageDriver:     i.Driver,
			DockerRootDir:     i.DockerRootDir,
		},
	})
}

// environment looks up the {env} path value and its Docker client, answering the
// request itself when that fails.
func (s *server) environment(w http.ResponseWriter, r *http.Request) (environments.Environment, docker.Client, bool) {
	id, err := strconv.ParseInt(r.PathValue("env"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "environment not found")
		return environments.Environment{}, nil, false
	}
	env, err := s.envs.Get(r.Context(), id)
	if errors.Is(err, environments.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "environment not found")
		return environments.Environment{}, nil, false
	}
	if err != nil {
		s.internalError(w, "get environment", err)
		return environments.Environment{}, nil, false
	}
	// A user sees only the environments an admin gave them; any other is missing.
	if u, ok := userFrom(r.Context()); ok && !isAdmin(u) {
		allowed, err := s.envs.HasAccess(r.Context(), u.ID, env.ID)
		if err != nil {
			s.internalError(w, "environment access", err)
			return environments.Environment{}, nil, false
		}
		if !allowed {
			s.writeError(w, http.StatusNotFound, "environment not found")
			return environments.Environment{}, nil, false
		}
	}
	c, err := s.envs.Client(env)
	if err != nil {
		s.internalError(w, "environment client", err)
		return environments.Environment{}, nil, false
	}
	return env, c, true
}

// dockerError answers a failed Docker call without the daemon's message, which can
// carry secrets (registry credentials, environment variables); the log keeps it.
func (s *server) dockerError(w http.ResponseWriter, r *http.Request, env environments.Environment, msg string, err error) {
	var inf *internalFailure
	switch {
	case errors.As(err, &inf):
		s.internalError(w, inf.msg, inf.err)
	case r.Context().Err() != nil:
		// The client went away; nothing to answer.
		return
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn(msg, "env", env.ID, "err", err)
		s.writeError(w, http.StatusGatewayTimeout, "the environment's Docker host did not answer in time")
	case docker.Unreachable(err):
		s.log.Warn(msg, "env", env.ID, "err", err)
		s.writeError(w, http.StatusServiceUnavailable, "the environment is offline")
	default:
		s.log.Error(msg, "env", env.ID, "err", err)
		s.writeError(w, http.StatusBadGateway, "the environment's Docker host returned an error")
	}
}
