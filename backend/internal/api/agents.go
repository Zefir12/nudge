package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

// agentImage is the image the docker run command starts; the agent is the same binary.
const agentImage = "registrydedic.zefirlabs.net/nudge:latest"

// hostHeader matches the host names and ports allowed into the docker run command, which
// users paste into a shell.
var hostHeader = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^\[[0-9A-Fa-f:.]+\](:[0-9]{1,5})?$`)

type joinTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	// DockerRun starts the agent on the remote host with this token.
	DockerRun string `json:"docker_run"`
}

type createdEnvironmentResponse struct {
	environmentResponse
	JoinToken joinTokenResponse `json:"join_token"`
}

type enrollResponse struct {
	EnvironmentID int64  `json:"environment_id"`
	AgentID       string `json:"agent_id"`
	AgentSecret   string `json:"agent_secret"`
}

func (s *server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Kind != environments.KindAgent {
		s.writeError(w, http.StatusBadRequest, `kind must be "agent"; the local environment is created automatically`)
		return
	}
	env, jt, err := s.envs.CreateAgent(r.Context(), req.Name)
	var inputErr *environments.InputError
	switch {
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
		return
	case errors.Is(err, environments.ErrNameTaken):
		s.writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		s.internalError(w, "create environment", err)
		return
	}
	s.log.Info("agent environment created", "env", env.ID, "name", env.Name, "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, createdEnvironmentResponse{
		environmentResponse: toEnvironmentResponse(env, false),
		JoinToken:           toJoinTokenResponse(r, jt),
	})
}

func (s *server) createJoinToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.envID(w, r)
	if !ok {
		return
	}
	jt, err := s.envs.RegenerateJoinToken(r.Context(), id)
	if s.agentError(w, err, "regenerate join token") {
		return
	}
	s.log.Info("join token created", "env", id, "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, toJoinTokenResponse(r, jt))
}

func (s *server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := s.envID(w, r)
	if !ok {
		return
	}
	if s.agentError(w, s.envs.Revoke(r.Context(), id), "revoke agent") {
		return
	}
	s.log.Info("agent revoked", "env", id, "ip", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// enrollAgent is public: the join token is the credential.
func (s *server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JoinToken string `json:"join_token"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	creds, err := s.envs.Enroll(r.Context(), req.JoinToken)
	if errors.Is(err, environments.ErrInvalidJoinToken) {
		s.log.Warn("agent enroll refused", "ip", clientIP(r))
		s.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "enroll agent", err)
		return
	}
	s.log.Info("agent enrolled", "env", creds.EnvironmentID, "agent", creds.AgentID, "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, enrollResponse{EnvironmentID: creds.EnvironmentID, AgentID: creds.AgentID, AgentSecret: creds.Secret})
}

// envID parses the {env} path value, answering 404 itself when it isn't a number.
func (s *server) envID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("env"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "environment not found")
		return 0, false
	}
	return id, true
}

// agentError answers err from an agent environment call; it reports whether it did.
func (s *server) agentError(w http.ResponseWriter, err error, msg string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, environments.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, environments.ErrNotAgent):
		s.writeError(w, http.StatusConflict, "not an agent environment")
	default:
		s.internalError(w, msg, err)
	}
	return true
}

func toJoinTokenResponse(r *http.Request, jt environments.JoinToken) joinTokenResponse {
	return joinTokenResponse{Token: jt.Token, ExpiresAt: jt.ExpiresAt.UTC(), DockerRun: dockerRun(serverURL(r), jt.Token)}
}

// serverURL guesses the URL agents reach the server at from the request.
// TODO(#12): use the configured public URL once there is one.
func serverURL(r *http.Request) string {
	if !hostHeader.MatchString(r.Host) {
		return "https://<server>"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func dockerRun(server, token string) string {
	return fmt.Sprintf("docker run -d --name nudge-agent --restart unless-stopped "+
		"-v /var/run/docker.sock:/var/run/docker.sock -v nudge-agent:/data "+
		"-e NUDGE_SERVER_URL=%s -e NUDGE_JOIN_TOKEN=%s %s agent", server, token, agentImage)
}
