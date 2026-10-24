package api

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// The list and inspect handlers of containers, images, volumes and networks answer with
// nudge's own types, not Docker's: those change between API versions and carry fields
// (driver options, registry details) that can hold secrets.

var (
	// objectID matches container, volume and network IDs and names.
	objectID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)
	// imageRef also allows references such as ghcr.io/org/app:1.2 and sha256:… IDs.
	imageRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:@/-]{0,254}$`)
)

// dockerCall looks up the environment, runs call with a bounded context and answers with
// its result; a missing object is a 404 "<what> not found".
func (s *server) dockerCall(w http.ResponseWriter, r *http.Request, what string, call func(ctx context.Context, c docker.Client) (any, error)) {
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dockerTimeout)
	defer cancel()
	resp, err := call(ctx, c)
	if docker.NotFound(err) {
		s.envs.Seen(r.Context(), &env)
		s.writeError(w, http.StatusNotFound, what+" not found")
		return
	}
	if err != nil {
		s.dockerError(w, r, env, what, err)
		return
	}
	s.envs.Seen(r.Context(), &env)
	s.writeJSON(w, http.StatusOK, resp)
}

// pathID returns the {id} path value if it matches pattern, else answers 404 itself.
func (s *server) pathID(w http.ResponseWriter, r *http.Request, pattern *regexp.Regexp, what string) (string, bool) {
	id := r.PathValue("id")
	if !pattern.MatchString(id) || strings.Contains(id, "..") {
		s.writeError(w, http.StatusNotFound, what+" not found")
		return "", false
	}
	return id, true
}

// unixTime converts Docker's Unix seconds, where 0 means unknown.
func unixTime(sec int64) *time.Time {
	if sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

// parseTime converts Docker's RFC 3339 strings; empty, invalid and zero times (a
// container that never started) are unknown.
func parseTime(s string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() || t.Year() <= 1 {
		return nil
	}
	t = t.UTC()
	return &t
}

// nonNil keeps empty lists and maps as [] and {} in JSON instead of null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// sortedKeys returns a set's keys in order, such as exposed ports or volumes.
func sortedKeys[K ~string, V any](m map[K]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	return keys
}

// sortBy sorts a list response by key so its order is stable.
func sortBy[T any](items []T, key func(T) string) {
	slices.SortFunc(items, func(a, b T) int { return strings.Compare(key(a), key(b)) })
}
