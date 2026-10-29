package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
)

const (
	// WatchInterval is how often the watcher looks at the environments: it pings each,
	// and opens the event stream of those online that have none.
	WatchInterval = 30 * time.Second
	// OfflineGrace is how long an agent stays offline before it is notified, so a
	// restart or a short network cut stays quiet.
	OfflineGrace = 2 * time.Minute
	// MaxPings bounds the environments pinged at once.
	MaxPings = 8
	// killWindow is how long after a kill a container's exit counts as asked for: the
	// stop timeout before SIGKILL is 10 seconds by default.
	killWindow = 5 * time.Minute
	// maxKilled bounds the kills remembered per environment.
	maxKilled = 1000
)

// reloadSignals are sent to running containers to reload or rotate logs, not to stop
// them (SIGHUP, SIGUSR1, SIGUSR2), so an exit after one is still a failure.
var reloadSignals = map[string]bool{"1": true, "10": true, "12": true}

// WatchEnvs is what the watcher needs from the environments.
type WatchEnvs interface {
	List(ctx context.Context) ([]environments.Environment, error)
	Client(env environments.Environment) (docker.Client, error)
	Online(ctx context.Context, env *environments.Environment) bool
}

// Sender queues a notification; *Service is one.
type Sender interface {
	Send(ctx context.Context, key string, window time.Duration, m Message) (bool, error)
	Recover(ctx context.Context, key string, m Message) (bool, error)
}

// WatchOptions are the Watcher's dependencies.
type WatchOptions struct {
	Envs  WatchEnvs
	Hub   *events.Hub
	Send  Sender
	Log   *slog.Logger
	Clock Clock
}

// Watcher notifies failed containers, from each environment's Docker events, and agents
// that stay offline, and their recoveries. Events while an environment's stream is down
// (until the next look) are missed.
type Watcher struct {
	envs  WatchEnvs
	hub   *events.Hub
	send  Sender
	log   *slog.Logger
	clock Clock

	// The fields below are owned by Run's goroutine.
	wg sync.WaitGroup
	// streams are the environments' event readers, keyed by environment id (bounded by
	// the environments table); done is closed when the reader ends.
	streams map[int64]stream
	// agents are the connected agent environments' outages, keyed by environment id.
	agents map[int64]outage
}

type stream struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// outage is when an agent was first seen offline and last notified; both are zero
// while it is online.
type outage struct {
	since    time.Time
	notified time.Time
}

// NewWatcher returns a Watcher; Run starts it.
func NewWatcher(opts WatchOptions) *Watcher {
	w := &Watcher{
		envs: opts.Envs, hub: opts.Hub, send: opts.Send, log: opts.Log, clock: opts.Clock,
		streams: map[int64]stream{}, agents: map[int64]outage{},
	}
	if w.log == nil {
		w.log = slog.New(slog.DiscardHandler)
	}
	if w.clock == nil {
		w.clock = realClock{}
	}
	return w
}

// Run looks at the environments every WatchInterval until ctx ends, then stops the
// event readers.
func (w *Watcher) Run(ctx context.Context) {
	defer w.stop()
	for {
		w.look(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.clock.After(WatchInterval):
		}
	}
}

func (w *Watcher) stop() {
	for _, st := range w.streams {
		st.cancel()
	}
	w.wg.Wait()
}

// look pings every environment, notifies agents offline past OfflineGrace and starts an
// event reader for each online environment that has none.
func (w *Watcher) look(ctx context.Context) {
	list, err := w.envs.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("notify: list environments", "err", err)
		}
		return
	}
	online := make([]bool, len(list))
	var wg sync.WaitGroup
	slots := make(chan struct{}, MaxPings)
	for i := range list {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			online[i] = w.envs.Online(ctx, &list[i])
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	seen := map[int64]bool{}
	for i, env := range list {
		seen[env.ID] = true
		if env.Kind == environments.KindAgent {
			w.checkAgent(ctx, env, online[i])
		}
		st, ok := w.streams[env.ID]
		if ok {
			select {
			case <-st.done:
				delete(w.streams, env.ID)
				ok = false
			default:
			}
		}
		if online[i] && !ok {
			w.startStream(ctx, env)
		}
	}
	// Deleted environments.
	for id, st := range w.streams {
		if !seen[id] {
			st.cancel()
			delete(w.streams, id)
		}
	}
	for id := range w.agents {
		if !seen[id] {
			delete(w.agents, id)
		}
	}
}

// checkAgent tracks how long an agent has been offline, notifies it after OfflineGrace
// and again every OfflineWindow, and its return. Agents that never connected aren't
// watched: they have nothing to lose yet.
func (w *Watcher) checkAgent(ctx context.Context, env environments.Environment, online bool) {
	if !env.Enrolled || env.LastSeenAt == nil {
		delete(w.agents, env.ID)
		return
	}
	key := "offline/" + strconv.FormatInt(env.ID, 10)
	o, known := w.agents[env.ID]
	if online {
		// Not known yet: an outage may have been notified before nudge restarted. A
		// recovery that failed is tried again at the next look.
		if !known || !o.notified.IsZero() {
			if !w.recovered(ctx, key, Message{
				Event:       EventAgentOnline,
				Text:        fmt.Sprintf("Agent environment %s is back online", env.Name),
				Environment: envRef(env),
			}) {
				return
			}
		}
		w.agents[env.ID] = outage{}
		return
	}
	now := w.clock.Now()
	if o.since.IsZero() {
		w.agents[env.ID] = outage{since: now}
		return
	}
	if now.Sub(o.since) < OfflineGrace || (!o.notified.IsZero() && now.Sub(o.notified) < OfflineWindow) {
		return
	}
	m := Message{
		Event:       EventAgentOffline,
		Text:        fmt.Sprintf("Agent environment %s is offline, last seen %s", env.Name, env.LastSeenAt.UTC().Format(time.RFC3339)),
		Environment: envRef(env),
	}
	// A failed send is tried again at the next look.
	if w.notify(ctx, key, OfflineWindow, m) {
		o.notified = now
		w.agents[env.ID] = o
	}
}

func (w *Watcher) startStream(ctx context.Context, env environments.Environment) {
	c, err := w.envs.Client(env)
	if err != nil {
		w.log.Warn("notify: environment client", "env", env.ID, "err", err)
		return
	}
	sub, err := w.hub.SubscribeReserved(env.ID, "notify", c)
	if err != nil {
		w.log.Warn("notify: watch events", "env", env.ID, "err", err)
		return
	}
	sctx, cancel := context.WithCancel(ctx)
	st := stream{cancel: cancel, done: make(chan struct{})}
	w.streams[env.ID] = st
	w.wg.Go(func() {
		defer close(st.done)
		defer sub.Close()
		w.read(sctx, env, sub)
	})
}

// read handles env's events until its stream ends or ctx is done.
func (w *Watcher) read(ctx context.Context, env environments.Environment, sub *events.Subscription) {
	// killed holds the containers asked to stop, by ID, so their exit isn't a failure.
	killed := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.C():
			if !ok {
				return
			}
			w.handle(ctx, env, killed, e)
		}
	}
}

func (w *Watcher) handle(ctx context.Context, env environments.Environment, killed map[string]time.Time, e events.Event) {
	if e.Type != "container" {
		return
	}
	now := w.clock.Now()
	name := e.Name
	if name == "" {
		name = e.ID
	}
	c := &Container{ID: e.ID, Name: name, Image: e.Attributes["image"]}
	key := "/" + strconv.FormatInt(env.ID, 10) + "/" + name
	switch e.Action {
	case "kill":
		if reloadSignals[e.Attributes["signal"]] {
			return
		}
		if len(killed) >= maxKilled {
			for id, at := range killed {
				if now.Sub(at) > killWindow {
					delete(killed, id)
				}
			}
			if len(killed) >= maxKilled {
				clear(killed)
			}
		}
		killed[e.ID] = now
	case "die":
		at, wasKilled := killed[e.ID]
		delete(killed, e.ID)
		code, err := strconv.Atoi(e.Attributes["exitCode"])
		if err != nil || code == 0 || (wasKilled && now.Sub(at) <= killWindow) {
			return
		}
		c.ExitCode = &code
		w.notify(ctx, "exited"+key, ProblemWindow, Message{
			Event:       EventExited,
			Text:        fmt.Sprintf("Container %s on %s exited with code %d", name, env.Name, code),
			Environment: envRef(env),
			Container:   c,
		})
	case "health_status: unhealthy":
		w.notify(ctx, "unhealthy"+key, ProblemWindow, Message{
			Event:       EventUnhealthy,
			Text:        fmt.Sprintf("Container %s on %s is unhealthy", name, env.Name),
			Environment: envRef(env),
			Container:   c,
		})
	case "health_status: healthy":
		w.recovered(ctx, "unhealthy"+key, Message{
			Event:       EventHealthy,
			Text:        fmt.Sprintf("Container %s on %s is healthy again", name, env.Name),
			Environment: envRef(env),
			Container:   c,
		})
	}
}

// notify sends m unless key was sent within window, and reports whether that worked
// (a repeat kept quiet counts).
func (w *Watcher) notify(ctx context.Context, key string, window time.Duration, m Message) bool {
	if _, err := w.send.Send(ctx, key, window, m); err != nil {
		if ctx.Err() == nil {
			w.log.Warn("notification not sent", "event", m.Event, "err", err)
		}
		return false
	}
	return true
}

// recovered sends m if the problem under key was notified, and reports whether that
// worked (no problem to recover counts).
func (w *Watcher) recovered(ctx context.Context, key string, m Message) bool {
	if _, err := w.send.Recover(ctx, key, m); err != nil {
		if ctx.Err() == nil {
			w.log.Warn("notification not sent", "event", m.Event, "err", err)
		}
		return false
	}
	return true
}
