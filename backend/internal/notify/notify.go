// Package notify sends notifications to webhooks: image updates, failed containers and
// offline agents, and their recoveries. One worker posts each notification as JSON to
// every enabled webhook that receives its event, a few targets at once, so a slow target
// delays the others but never a caller. A problem is sent once per window (keys in
// notify_sent), so a crash loop or a flapping agent doesn't spam; its recovery is sent
// only if the problem was, and ends the window.
// Webhook URLs and header values are sealed with a secretbox.Box: chat webhooks put their
// token in the URL, so neither leaves the package except in a delivery.
package notify

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
)

// Events a notification can be.
const (
	EventTest            = "test"
	EventUpdateAvailable = "update.available"
	EventUnhealthy       = "container.unhealthy"
	EventHealthy         = "container.healthy"
	EventExited          = "container.exited"
	EventAgentOffline    = "agent.offline"
	EventAgentOnline     = "agent.online"
)

// How long a problem stays quiet after its notification.
const (
	// UpdateWindow covers one registry digest of an image: in practice once.
	UpdateWindow = 90 * 24 * time.Hour
	// ProblemWindow covers one container's failures.
	ProblemWindow = time.Hour
	// OfflineWindow is how often an agent that stays offline is notified again.
	OfflineWindow = 24 * time.Hour
	// RecoveryWindow is how long after its window a problem can still recover: a
	// container unhealthy for hours gets no new notice, but its recovery is still sent.
	RecoveryWindow = 24 * time.Hour
)

const (
	// QueueSize bounds the notifications waiting for delivery; more are dropped.
	QueueSize = 100
	// Attempts is how often a delivery is tried before it fails.
	Attempts = 3
	// RetryDelay is the wait before the second attempt, doubled for each further one.
	RetryDelay = 5 * time.Second
	// MaxDeliveries bounds the targets one notification is posted to at once.
	MaxDeliveries = 4

	sendTimeout = 10 * time.Second
	pruneEvery  = time.Hour
	maxImages   = 100
)

// ErrQueueFull is returned by Send when the delivery queue is full.
var ErrQueueFull = errors.New("notification queue is full")

// Message is a notification's JSON body.
type Message struct {
	Event string    `json:"event"`
	Time  time.Time `json:"time"`
	// Text is a one-line summary for chat targets and logs.
	Text        string        `json:"message"`
	Environment *Environment  `json:"environment,omitempty"`
	Container   *Container    `json:"container,omitempty"`
	Images      []ImageUpdate `json:"images,omitempty"`
}

// Environment names the environment a notification is about.
type Environment struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Container is the container a notification is about.
type Container struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

// ImageUpdate is one image with a newer version in its registry.
type ImageUpdate struct {
	Image        string `json:"image"`
	LocalDigest  string `json:"local_digest"`
	RemoteDigest string `json:"remote_digest"`
}

// DeliveryError is a failed delivery, with a message safe to show.
type DeliveryError struct {
	Msg string
	// retry is false when another attempt would fail the same way (a 4xx).
	retry bool
}

func (e *DeliveryError) Error() string { return e.Msg }

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
	DB  *sql.DB
	Box *secretbox.Box
	Log *slog.Logger
	// Version goes into the User-Agent.
	Version string
	Clock   Clock
	// Transport sends the requests; nil means http.DefaultTransport.
	Transport http.RoundTripper
}

// Service stores the webhooks and delivers notifications to them.
type Service struct {
	db        *sql.DB
	box       *secretbox.Box
	log       *slog.Logger
	userAgent string
	clock     Clock
	client    *http.Client
	queue     chan queued
}

// queued is a notification waiting for delivery and the keys it claimed, released if
// no webhook gets it, so the problem is sent again rather than lost for its window.
type queued struct {
	m    Message
	keys []string
}

// New returns a Service; Run delivers what Send queues.
func New(opts Options) *Service {
	s := &Service{
		db: opts.DB, box: opts.Box, log: opts.Log, clock: opts.Clock,
		userAgent: "nudge/" + opts.Version,
		queue:     make(chan queued, QueueSize),
		client: &http.Client{
			Transport: opts.Transport,
			Timeout:   sendTimeout,
			// A redirect would take the secret header to another URL.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.clock == nil {
		s.clock = realClock{}
	}
	return s
}

// Send queues m for every enabled webhook unless key was sent within window; an empty
// key is always sent. It reports whether m was queued.
func (s *Service) Send(ctx context.Context, key string, window time.Duration, m Message) (bool, error) {
	var keys []string
	if key != "" {
		ok, err := s.claim(ctx, key, window)
		if err != nil || !ok {
			return false, err
		}
		keys = []string{key}
	}
	if err := s.enqueue(ctx, m, keys); err != nil {
		return false, err
	}
	return true, nil
}

// enqueue queues m, releasing its keys if the queue is full.
func (s *Service) enqueue(ctx context.Context, m Message, keys []string) error {
	if m.Time.IsZero() {
		m.Time = s.clock.Now().UTC()
	}
	select {
	case s.queue <- queued{m: m, keys: keys}:
		return nil
	default:
		s.release(ctx, keys)
		return ErrQueueFull
	}
}

// release forgets that keys were sent; best effort.
func (s *Service) release(ctx context.Context, keys []string) {
	for _, key := range keys {
		if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM notify_sent WHERE key = ?`, key); err != nil {
			s.log.Warn("release notification", "key", key, "err", err)
		}
	}
}

// Recover queues m, a recovery, if the problem notification under key was sent and its
// window ended at most RecoveryWindow ago; it forgets key, so the next problem is sent at
// once. It reports whether m was queued. A problem still waiting for delivery counts as
// sent: if no webhook gets it after all, its recovery may arrive alone.
func (s *Service) Recover(ctx context.Context, key string, m Message) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notify_sent WHERE key = ? AND expires_at > ?`,
		key, s.clock.Now().Add(-RecoveryWindow).UnixMilli())
	if err != nil {
		return false, fmt.Errorf("recovery %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("recovery %s: %w", key, err)
	}
	if n == 0 {
		return false, nil
	}
	if err := s.enqueue(ctx, m, nil); err != nil {
		return false, err
	}
	return true, nil
}

// claim records that key is sent now and reports whether its last send has expired.
func (s *Service) claim(ctx context.Context, key string, window time.Duration) (bool, error) {
	now := s.clock.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO notify_sent (key, expires_at) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET expires_at = excluded.expires_at WHERE notify_sent.expires_at <= ?`,
		key, now.Add(window).UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("notification %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("notification %s: %w", key, err)
	}
	return n == 1, nil
}

// Prune forgets the sends whose window and RecoveryWindow have passed.
func (s *Service) Prune(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notify_sent WHERE expires_at <= ?`, s.clock.Now().Add(-RecoveryWindow).UnixMilli()); err != nil {
		return fmt.Errorf("prune notifications: %w", err)
	}
	return nil
}

// Run delivers the queued notifications and prunes old sends until ctx ends.
func (s *Service) Run(ctx context.Context) {
	prune := s.clock.After(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune:
			if err := s.Prune(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("prune notifications", "err", err)
			}
			prune = s.clock.After(pruneEvery)
		case q := <-s.queue:
			s.deliver(ctx, q)
		}
	}
}

// deliver sends q, releasing its keys if no webhook got it.
func (s *Service) deliver(ctx context.Context, q queued) {
	if !s.deliverAll(ctx, q.m) {
		s.release(ctx, q.keys)
	}
}

// deliverAll posts m to every enabled webhook that receives its event, MaxDeliveries at
// once, and reports whether any of them got it.
func (s *Service) deliverAll(ctx context.Context, m Message) bool {
	targets, err := s.targets(ctx, 0, m.Event)
	if err != nil {
		s.log.Warn("notification not sent", "event", m.Event, "err", err)
		return false
	}
	body, err := json.Marshal(m)
	if err != nil {
		s.log.Warn("notification not sent", "event", m.Event, "err", err)
		return false
	}
	var delivered atomic.Bool
	var wg sync.WaitGroup
	slots := make(chan struct{}, MaxDeliveries)
	for _, t := range targets {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			if s.deliverOne(ctx, t, m.Event, body) {
				delivered.Store(true)
			}
		})
	}
	wg.Wait()
	return delivered.Load()
}

// deliverOne posts body to t, retrying a few times, and reports whether t got it.
func (s *Service) deliverOne(ctx context.Context, t target, event string, body []byte) bool {
	delay := RetryDelay
	for attempt := 1; ; attempt++ {
		err := s.post(ctx, t, event, body)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if attempt == Attempts || !err.retry {
			s.log.Warn("webhook delivery failed", "webhook", t.id, "event", event, "err", err)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-s.clock.After(delay):
		}
		delay *= 2
	}
}

// Test posts a test notification to webhook id now and returns why it failed, if it did.
func (s *Service) Test(ctx context.Context, id int64) error {
	targets, err := s.targets(ctx, id, "")
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return ErrNotFound
	}
	m := Message{Event: EventTest, Time: s.clock.Now().UTC(), Text: "Test notification from nudge"}
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("test webhook: %w", err)
	}
	if err := s.post(ctx, targets[0], m.Event, body); err != nil {
		return err
	}
	return nil
}

// post sends one delivery and records its outcome. The returned error is a
// *DeliveryError: the transport's error names the URL, which may hold a secret, so only
// its kind is kept.
func (s *Service) post(ctx context.Context, t target, event string, body []byte) *DeliveryError {
	derr := s.do(ctx, t, event, body)
	if derr == nil {
		s.recordDelivery(context.WithoutCancel(ctx), t.id, "")
		return nil
	}
	s.recordDelivery(context.WithoutCancel(ctx), t.id, derr.Msg)
	return derr
}

func (s *Service) do(ctx context.Context, t target, event string, body []byte) *DeliveryError {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return &DeliveryError{Msg: "invalid webhook url"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("X-Nudge-Event", event)
	if t.headerName != "" {
		req.Header.Set(t.headerName, t.headerValue)
	}
	resp, err := s.client.Do(req) // #nosec G704 -- the URL is an admin's webhook target by design
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &DeliveryError{Msg: "the webhook did not answer in time", retry: true}
		}
		return &DeliveryError{Msg: "could not reach the webhook", retry: true}
	}
	_ = resp.Body.Close()
	if code := resp.StatusCode; code < 200 || code > 299 {
		// A 4xx is the request's fault, except a timeout or a rate limit.
		retry := code >= 500 || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests
		return &DeliveryError{Msg: "the webhook answered with status " + strconv.Itoa(code), retry: retry}
	}
	return nil
}

// UpdatesChecked notifies the images of an update check whose registry digest is new:
// one notification per check, each digest once. It fits updates.Options.Checked.
func (s *Service) UpdatesChecked(ctx context.Context, env environments.Environment, results []updates.Result) {
	var images []ImageUpdate
	var keys []string
	for _, r := range results {
		if r.Status != updates.Available || r.RemoteDigest == "" || len(images) == maxImages {
			continue
		}
		key := "update/" + strconv.FormatInt(env.ID, 10) + "/" + r.Image + "/" + r.RemoteDigest
		ok, err := s.claim(ctx, key, UpdateWindow)
		if err != nil {
			s.log.Warn("update notification", "env", env.ID, "err", err)
			s.release(ctx, keys)
			return
		}
		if ok {
			keys = append(keys, key)
			images = append(images, ImageUpdate{Image: r.Image, LocalDigest: r.LocalDigest, RemoteDigest: r.RemoteDigest})
		}
	}
	if len(images) == 0 {
		return
	}
	text := fmt.Sprintf("%d image updates available on %s", len(images), env.Name)
	if len(images) == 1 {
		text = fmt.Sprintf("Update available for %s on %s", images[0].Image, env.Name)
	}
	m := Message{Event: EventUpdateAvailable, Text: text, Environment: envRef(env), Images: images}
	if err := s.enqueue(ctx, m, keys); err != nil {
		s.log.Warn("update notification", "env", env.ID, "err", err)
	}
}

func envRef(env environments.Environment) *Environment {
	return &Environment{ID: env.ID, Name: env.Name}
}
