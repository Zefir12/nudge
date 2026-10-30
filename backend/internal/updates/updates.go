// Package updates finds images with a newer version in their registry: for every image an
// environment's containers run by tag, it asks that environment's daemon for the digest
// the registry has for the tag and compares it with the digests the local image was
// pulled as. Going through the daemon means agent environments are checked the same way
// and the daemon's registry mirrors and proxy apply.
package updates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
)

const (
	// Concurrency is how many registry lookups run at once.
	Concurrency = 4
	// RegistryGap is the least time between two lookups at the same registry.
	RegistryGap = 500 * time.Millisecond
	// MaxImages bounds the images one check looks up per environment.
	MaxImages = 500

	listTimeout   = 30 * time.Second
	lookupTimeout = 30 * time.Second
)

// Status is the outcome of one image's check.
type Status string

const (
	// Available means the registry has a different digest for the tag.
	Available Status = "available"
	// Current means the image is what the registry has for the tag.
	Current Status = "current"
	// Unknown means the check couldn't tell; Result.Error says why.
	Unknown Status = "unknown"
)

// ErrRunning is returned when a check is started while another one runs.
var ErrRunning = errors.New("an update check is already running")

// ErrStore wraps a failure to save the results, as opposed to one of the environment.
var ErrStore = errors.New("save image updates")

// Result is the last check of one image reference (as containers name it) and the image
// ID the containers ran.
type Result struct {
	Image   string
	ImageID string
	Status  Status
	// LocalDigest is the digest the image was pulled as, RemoteDigest the registry's for
	// the tag; either is empty when unknown.
	LocalDigest  string
	RemoteDigest string
	// Error says why the status is Unknown, safe to show (no daemon message).
	Error     string
	CheckedAt time.Time
}

// Envs is what checks need from the environments.
type Envs interface {
	List(ctx context.Context) ([]environments.Environment, error)
	Client(env environments.Environment) (docker.Client, error)
}

// Clock is the time source; tests skip the waits.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options are the Service's dependencies.
type Options struct {
	DB   *sql.DB
	Log  *slog.Logger
	Envs Envs
	// Auth returns the stored credentials for the registry a reference names.
	Auth  func(ctx context.Context, ref string) (registries.Auth, bool, error)
	Clock Clock
	// Checked, if set, gets the results of each environment's check once they're saved.
	Checked func(ctx context.Context, env environments.Environment, results []Result)
}

// Service checks environments for image updates and keeps the last results.
type Service struct {
	db      *sql.DB
	log     *slog.Logger
	envs    Envs
	auth    func(ctx context.Context, ref string) (registries.Auth, bool, error)
	clock   Clock
	checked func(ctx context.Context, env environments.Environment, results []Result)
	// running holds a token while a check runs; one check at a time.
	running chan struct{}
}

// New returns a Service.
func New(opts Options) *Service {
	s := &Service{db: opts.DB, log: opts.Log, envs: opts.Envs, auth: opts.Auth, clock: opts.Clock, checked: opts.Checked, running: make(chan struct{}, 1)}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.clock == nil {
		s.clock = realClock{}
	}
	if s.auth == nil {
		s.auth = func(context.Context, string) (registries.Auth, bool, error) { return registries.Auth{}, false, nil }
	}
	return s
}

// List returns the last results for an environment, by image.
func (s *Service) List(ctx context.Context, envID int64) ([]Result, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT image, image_id, status, local_digest, remote_digest, error, checked_at
		FROM image_updates WHERE environment_id = ? ORDER BY image, image_id`, envID)
	if err != nil {
		return nil, fmt.Errorf("list image updates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Result{}
	for rows.Next() {
		var r Result
		var checked int64
		if err := rows.Scan(&r.Image, &r.ImageID, &r.Status, &r.LocalDigest, &r.RemoteDigest, &r.Error, &checked); err != nil {
			return nil, fmt.Errorf("list image updates: %w", err)
		}
		r.CheckedAt = time.UnixMilli(checked).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list image updates: %w", err)
	}
	return out, nil
}

// Check checks one environment now and returns its results; ErrRunning if a check runs.
// The previous results are kept when the environment can't be listed.
func (s *Service) Check(ctx context.Context, env environments.Environment) ([]Result, error) {
	if !s.lock() {
		return nil, ErrRunning
	}
	defer s.unlock()
	return s.check(ctx, env, newRun(s.clock))
}

// CheckAll checks every environment, one after the other. An environment that fails is
// logged and skipped.
func (s *Service) CheckAll(ctx context.Context) error {
	if !s.lock() {
		return ErrRunning
	}
	defer s.unlock()
	envs, err := s.envs.List(ctx)
	if err != nil {
		return fmt.Errorf("update check: %w", err)
	}
	r := newRun(s.clock)
	for _, env := range envs {
		if _, err := s.check(ctx, env, r); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if docker.Unreachable(err) {
				s.log.Info("update check skipped: environment offline", "env", env.ID)
				continue
			}
			s.log.Warn("update check", "env", env.ID, "err", err)
		}
	}
	return nil
}

// Run checks every environment after delay and then every interval until ctx ends.
func (s *Service) Run(ctx context.Context, delay, interval time.Duration) {
	wait := delay
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(wait):
		}
		if err := s.CheckAll(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, ErrRunning) {
			s.log.Warn("update check", "err", err)
		}
		wait = interval
	}
}

func (s *Service) lock() bool {
	select {
	case s.running <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) unlock() { <-s.running }

// use is one reference and image ID the containers run.
// use is an image a container runs; moved is true when the daemon listed it by ID, its
// tag having moved on to another image.
type use struct {
	ref, id string
	moved   bool
}

func (s *Service) check(ctx context.Context, env environments.Environment, r *run) ([]Result, error) {
	c, err := s.envs.Client(env)
	if err != nil {
		return nil, fmt.Errorf("environment client: %w", err)
	}
	images, err := s.images(ctx, c)
	if err != nil {
		return nil, err
	}

	results := make([]Result, len(images))
	// Digests by image ID and lookups by reference are per environment: daemons differ in
	// mirrors, proxy and what they can reach.
	repoDigests := map[string][]string{}
	lookups := map[string]*lookup{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, Concurrency)
	for i, img := range images {
		results[i] = Result{Image: img.ref, ImageID: img.id}
		local, err := s.localDigest(ctx, c, img, repoDigests)
		if err != nil {
			results[i].Status, results[i].Error = Unknown, err.Error()
			continue
		}
		results[i].LocalDigest = local
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			remote, err := shared(ctx, &mu, lookups, img.ref, func() (string, error) { return s.remoteDigest(ctx, c, img.ref, r) })
			res := &results[i]
			res.CheckedAt = s.clock.Now()
			switch {
			case err != nil:
				res.Status, res.Error = Unknown, err.Error()
			case remote == local:
				res.Status, res.RemoteDigest = Current, remote
			default:
				res.Status, res.RemoteDigest = Available, remote
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	for i := range results {
		if results[i].CheckedAt.IsZero() {
			results[i].CheckedAt = now
		}
	}
	if err := s.save(ctx, env.ID, results); err != nil {
		return nil, err
	}
	if s.checked != nil {
		s.checked(ctx, env, results)
	}
	return results, nil
}

// images returns the distinct references and image IDs the environment's containers
// run, leaving out those pinned to a digest or named by ID, which a tag can't update.
func (s *Service) images(ctx context.Context, c docker.Client) ([]use, error) {
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	list, err := c.ContainerList(lctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	seen := map[use]bool{}
	var out []use
	for _, ctr := range list.Items {
		img := use{ref: ctr.Image, id: ctr.ImageID}
		if imageID.MatchString(img.ref) && ctr.ID != "" {
			// The daemon lists the image by ID once its tag names a newer one (pulled,
			// not applied); the container's config still has the reference.
			img.ref, img.moved = s.createdFrom(lctx, c, ctr.ID, img.ref), true
		}
		if seen[img] || !byTag(img.ref) || img.id == "" {
			continue
		}
		if len(out) == MaxImages {
			s.log.Warn("update check: too many images, checking the first ones", "max", MaxImages)
			break
		}
		seen[img] = true
		out = append(out, img)
	}
	return out, nil
}

// createdFrom returns the reference container id was created from, or ref when it can't
// be inspected.
func (s *Service) createdFrom(ctx context.Context, c docker.Client, id, ref string) string {
	res, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || res.Container.Config == nil {
		s.log.Debug("update check: inspect container", "container", id, "err", err)
		return ref
	}
	return res.Container.Config.Image
}

// localDigest is the digest the image was pulled as from the reference's repository;
// empty when the image is gone after its tag moved.
func (s *Service) localDigest(ctx context.Context, c docker.Client, img use, repoDigests map[string][]string) (string, error) {
	digests, ok := repoDigests[img.id]
	if !ok {
		ictx, cancel := context.WithTimeout(ctx, lookupTimeout)
		res, err := c.ImageInspect(ictx, img.id)
		cancel()
		if img.moved && docker.NotFound(err) {
			// The containerd image store drops an image once its tag moves, though a
			// container still runs it. Any digest of the tag is newer than that.
			return "", nil
		}
		if err != nil {
			s.log.Warn("update check: inspect image", "image", img.ref, "err", err)
			return "", errors.New("the image couldn't be inspected")
		}
		digests = res.RepoDigests
		repoDigests[img.id] = digests
	}
	if dg := DigestFor(img.ref, digests); dg != "" {
		return dg, nil
	}
	return "", errors.New("the image has no registry digest (built locally?)")
}

// DigestFor picks the digest of ref's repository from an image's RepoDigests
// ("nginx@sha256:..."); empty if the image wasn't pulled from there.
func DigestFor(ref string, repoDigests []string) string {
	repo := repoOf(ref)
	for _, d := range repoDigests {
		name, dg, ok := strings.Cut(d, "@")
		if ok && repoOf(name) == repo {
			return dg
		}
	}
	return ""
}

// remoteDigest asks the daemon for the registry's digest of ref. Its errors are safe to
// show; the daemon's message is only logged.
func (s *Service) remoteDigest(ctx context.Context, c docker.Client, ref string, r *run) (string, error) {
	auth, found, err := s.auth(ctx, ref)
	if errors.Is(err, secretbox.ErrDecrypt) {
		s.log.Error("update check: registry credentials", "image", ref, "err", err)
		return "", errors.New("the stored registry password can't be decrypted")
	}
	if err != nil {
		s.log.Error("update check: registry credentials", "image", ref, "err", err)
		return "", errors.New("the registry credentials couldn't be read")
	}
	var opts client.DistributionInspectOptions
	if found {
		opts.EncodedRegistryAuth = auth.Header()
	}
	if err := r.limit.wait(ctx, registries.HostOf(ref)); err != nil {
		return "", errors.New("the check was cancelled")
	}
	dctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	res, err := c.DistributionInspect(dctx, ref, opts)
	if err == nil && res.Descriptor.Digest == "" {
		err = errors.New("empty digest")
	}
	if err == nil {
		return res.Descriptor.Digest.String(), nil
	}
	msg := err.Error()
	if found && auth.Password != "" {
		msg = strings.ReplaceAll(msg, auth.Password, "***")
	}
	s.log.Warn("update check: registry lookup", "image", ref, "registry_auth", found, "err", msg)
	switch {
	case ctx.Err() != nil:
		return "", errors.New("the check was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return "", errors.New("the registry didn't answer in time")
	case cerrdefs.IsNotFound(err):
		return "", errors.New("the registry has no such tag")
	case cerrdefs.IsUnauthorized(err), cerrdefs.IsPermissionDenied(err):
		return "", errors.New("the registry refused access (check its credentials under Registries)")
	default:
		return "", errors.New("the registry lookup failed")
	}
}

// save replaces the environment's results.
func (s *Service) save(ctx context.Context, envID int64, results []Result) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `DELETE FROM image_updates WHERE environment_id = ?`, envID); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	for _, r := range results {
		if _, err = tx.ExecContext(ctx, `INSERT INTO image_updates
			(environment_id, image, image_id, status, local_digest, remote_digest, error, checked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			envID, r.Image, r.ImageID, string(r.Status), r.LocalDigest, r.RemoteDigest, r.Error, r.CheckedAt.UnixMilli()); err != nil {
			return fmt.Errorf("%w: %w", ErrStore, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	return nil
}

// imageID matches a bare image ID, which containers show as their image once the tag
// they were created from moved on.
var imageID = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)

// byTag reports whether ref names an image by tag (an implicit latest included), the
// only kind a registry can have a newer version of.
func byTag(ref string) bool {
	return ref != "" && !strings.Contains(ref, "@") && !imageID.MatchString(ref)
}

// repoOf is the repository of a reference without tag or digest, in the short form the
// daemon uses for Docker Hub (nginx, not docker.io/library/nginx).
func repoOf(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		if rest, ok := strings.CutPrefix(name, p); ok {
			name = rest
			break
		}
	}
	if registries.HostOf(name) == registries.DockerHub {
		name = strings.TrimPrefix(name, "library/")
	}
	return name
}

// run is the state one check shares across environments: the per-registry rate limit.
type run struct {
	limit *limiter
}

func newRun(clock Clock) *run {
	return &run{limit: newLimiter(clock, RegistryGap)}
}

type lookup struct {
	done   chan struct{}
	digest string
	err    error
}

// shared returns the registry digest of ref from lookups, calling fetch only for the
// first caller; mu guards lookups.
func shared(ctx context.Context, mu *sync.Mutex, lookups map[string]*lookup, ref string, fetch func() (string, error)) (string, error) {
	mu.Lock()
	l, ok := lookups[ref]
	if !ok {
		l = &lookup{done: make(chan struct{})}
		lookups[ref] = l
	}
	mu.Unlock()
	if !ok {
		l.digest, l.err = fetch()
		close(l.done)
	}
	select {
	case <-l.done:
		return l.digest, l.err
	case <-ctx.Done():
		return "", errors.New("the check was cancelled")
	}
}

// limiter spaces requests to each registry by gap. It lives for one run, so its map is
// bounded by the references checked.
type limiter struct {
	clock Clock
	gap   time.Duration
	mu    sync.Mutex
	next  map[string]time.Time
}

func newLimiter(clock Clock, gap time.Duration) *limiter {
	return &limiter{clock: clock, gap: gap, next: map[string]time.Time{}}
}

// wait blocks until a request to host may go out.
func (l *limiter) wait(ctx context.Context, host string) error {
	l.mu.Lock()
	now := l.clock.Now()
	at := l.next[host]
	if at.Before(now) {
		at = now
	}
	l.next[host] = at.Add(l.gap)
	l.mu.Unlock()
	d := at.Sub(now)
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.clock.After(d):
		return nil
	}
}
