package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

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

func (s *server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := s.envs.List(r.Context())
	if err != nil {
		s.internalError(w, "list environments", err)
		return
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
	s.envs.Seen(r.Context(), &env)
	i := info.Info
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
	switch {
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
