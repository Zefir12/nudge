package metrics

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

func recvLive(t *testing.T, sub *LiveSubscription) Live {
	t.Helper()
	select {
	case l, ok := <-sub.C():
		if !ok {
			t.Fatalf("subscription ended: %v", sub.Err())
		}
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no live tick")
	}
	return Live{}
}

func names(l Live) []string {
	out := []string{}
	for _, c := range l.Containers {
		out = append(out, c.Name)
	}
	return out
}

// A live tick shows each subscriber only the containers in its scope, and their totals.
func TestLiveView(t *testing.T) {
	t.Parallel()
	s := Sample{CPUPercent: 10, MemoryUsage: 100, NetworkRx: 1, NetworkTx: 2, BlockRead: 3, BlockWrite: 4}
	tk := newTick(t0,
		[]container.Summary{{ID: "w1"}, {ID: "d1"}, {ID: "b2"}, {ID: "g1"}, {ID: "g2"}},
		[]Container{{ID: "w1", Name: "web", Sample: s}, {ID: "d1", Name: "db", Sample: s}, {ID: "b2", Name: "api", Sample: s}},
		map[string]string{"w1": "bob", "b2": "bob", "g2": "bob"})
	tests := []struct {
		name    string
		sc      Scope
		want    []string
		skipped int
	}{
		{"admin", Scope{All: true}, []string{"api", "db", "web"}, 2},
		{"owner", Scope{Owner: "bob"}, []string{"api", "web"}, 1},
		{"other user", Scope{Owner: "carol"}, []string{}, 0},
		{"no owner key", Scope{}, []string{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := tk.view(tt.sc)
			if !slices.Equal(names(l), tt.want) || l.Skipped != tt.skipped {
				t.Errorf("view = %v skipped %d, want %v skipped %d", names(l), l.Skipped, tt.want, tt.skipped)
			}
			n := float64(len(tt.want))
			if l.Total.CPUPercent != 10*n || l.Total.MemoryUsage != uint64(100*n) || l.Total.BlockWrite != uint64(4*n) {
				t.Errorf("total = %+v for %v containers", l.Total, n)
			}
			if !l.Time.Equal(t0) {
				t.Errorf("time = %v, want %v", l.Time, t0)
			}
		})
	}
}

// A watched environment is sampled every live interval and those ticks go to the
// subscribers only; once the last one leaves, sampling is back on the stored cadence.
func TestLiveCadence(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mt.svc.collectLoop(ctx, environments.Environment{ID: 1})
	}()
	stored := func() int { return mt.count(t, `SELECT COUNT(*) FROM environment_samples`) }
	if d := mt.clock.wait(t, 1); d != time.Minute {
		t.Fatalf("unwatched wait = %v, want the interval", d)
	}
	if n := stored(); n != 1 {
		t.Fatalf("stored ticks = %d, want 1", n)
	}

	sub, err := mt.svc.SubscribeLive(1, Scope{Owner: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	// The latest tick comes at once, in bob's scope.
	if l := recvLive(t, sub); !slices.Equal(names(l), []string{"web"}) || !l.Time.Equal(t0) {
		t.Errorf("first tick = %v at %v, want bob's web at t0", names(l), l.Time)
	}
	// The subscriber woke the collector, which now waits the live interval.
	if d := mt.clock.wait(t, 2); d != DefaultLiveInterval {
		t.Fatalf("watched wait = %v, want %v", d, DefaultLiveInterval)
	}
	mt.clock.tick(DefaultLiveInterval)
	if l := recvLive(t, sub); !l.Time.Equal(t0.Add(DefaultLiveInterval)) {
		t.Errorf("live tick at %v, want t0+5s", l.Time)
	}
	if d := mt.clock.wait(t, 3); d != DefaultLiveInterval {
		t.Fatalf("watched wait = %v, want %v", d, DefaultLiveInterval)
	}
	if n := stored(); n != 1 {
		t.Errorf("stored ticks = %d, want the live one left out", n)
	}

	sub.Close()
	mt.clock.tick(DefaultLiveInterval)
	// Unwatched again: the rest of the minute, then the stored tick and a full interval.
	if d := mt.clock.wait(t, 4); d != time.Minute-2*DefaultLiveInterval {
		t.Fatalf("wait after the last subscriber left = %v, want 50s", d)
	}
	mt.clock.tick(time.Minute - 2*DefaultLiveInterval)
	if d := mt.clock.wait(t, 5); d != time.Minute {
		t.Fatalf("wait after the stored tick = %v, want the interval", d)
	}
	if n := stored(); n != 2 {
		t.Errorf("stored ticks = %d, want 2", n)
	}
	cancel()
	<-done
}

// Subscriptions are refused while collection is off and over the bound; a slow one is
// dropped, and all end when their collector stops.
func TestLiveSubscriptions(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	if _, err := New(Options{DB: mt.db, Clock: mt.clock}).SubscribeLive(1, Scope{All: true}); !errors.Is(err, ErrOff) {
		t.Errorf("collection off: err = %v, want ErrOff", err)
	}

	_, unregister := mt.svc.live.register(1)
	slow, err := mt.svc.SubscribeLive(1, Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	for range liveBuffer + 1 {
		mt.svc.live.publish(1, &tick{time: t0})
	}
	got := 0
	for range slow.C() {
		got++
	}
	if got != liveBuffer || !errors.Is(slow.Err(), ErrSlow) {
		t.Errorf("slow: got %d ticks and err %v, want %d and ErrSlow", got, slow.Err(), liveBuffer)
	}

	var subs []*LiveSubscription
	for range maxLiveSubscribers {
		sub, err := mt.svc.SubscribeLive(1, Scope{All: true})
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, sub)
	}
	if _, err := mt.svc.SubscribeLive(1, Scope{All: true}); !errors.Is(err, ErrTooMany) {
		t.Errorf("over the bound: err = %v, want ErrTooMany", err)
	}
	unregister()
	for _, sub := range subs {
		for range sub.C() {
		}
		if !errors.Is(sub.Err(), ErrStopped) {
			t.Fatalf("after the collector stopped: err = %v, want ErrStopped", sub.Err())
		}
	}
	if mt.svc.live.watched(1) {
		t.Error("environment still watched")
	}

	mt.svc.live.close()
	if _, err := mt.svc.SubscribeLive(1, Scope{All: true}); !errors.Is(err, ErrStopped) {
		t.Errorf("after close: err = %v, want ErrStopped", err)
	}
}
