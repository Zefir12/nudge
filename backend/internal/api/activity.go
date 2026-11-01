package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/metrics"
)

// defaultActivityDays is how many days the activity answers when the request doesn't
// say.
const defaultActivityDays = 7

// environmentActivity answers the environment's container events per hour for the last
// days days (7 if absent) in the tz time zone (UTC if absent), oldest day first: every
// container's for an admin, a user's own containers' for a user.
func (s *server) environmentActivity(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	days := defaultActivityDays
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > metrics.MaxActivityDays {
			s.writeError(w, http.StatusBadRequest, metrics.ErrActivityDays.Error())
			return
		}
		days = n
	}
	loc := time.UTC
	if v := r.URL.Query().Get("tz"); v != "" {
		l, err := time.LoadLocation(v)
		// Local would be the server's zone, not one the client named.
		if err != nil || v == "Local" {
			s.writeError(w, http.StatusBadRequest, "tz: want a time zone such as Europe/Warsaw")
			return
		}
		loc = l
	}
	if s.activity == nil {
		s.writeError(w, http.StatusNotFound, "activity is not counted")
		return
	}
	sc, err := s.scopeOf(r.Context())
	if err != nil {
		s.internalError(w, "environment activity", err)
		return
	}
	out, err := s.activity.Days(r.Context(), env.ID, metrics.Scope{All: sc.all, Owner: sc.key}, days, loc)
	if err != nil {
		s.internalError(w, "environment activity", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"days": out})
}

// EventOwner returns the owner key of the container id if a user owns it (the owner
// label, unless its image sets one too; see ownsContainer), else "". attrs are its
// event's attributes, its labels: without the owner label Docker isn't asked. A
// container already gone is nobody's. The activity counter files events under it.
func EventOwner(ctx context.Context, c docker.Client, id string, attrs map[string]string) (string, error) {
	if attrs[ownerLabel] == "" {
		return "", nil
	}
	ct, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if docker.NotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect container: %w", err)
	}
	var labels map[string]string
	if ct.Container.Config != nil {
		labels = ct.Container.Config.Labels
	}
	key := labels[ownerLabel]
	if key == "" {
		return "", nil
	}
	ok, err := scope{key: key}.ownsContainer(ctx, c, labels, ct.Container.Image)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return key, nil
}

var _ metrics.EventOwnerFunc = EventOwner
