package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/hostload"
)

// fakeHost answers its readings in turn, then errors.
type fakeHost struct {
	readings []hostload.Reading
	errs     []error
}

func (f *fakeHost) read(context.Context, environments.Environment) (hostload.Reading, error) {
	if len(f.readings) == 0 {
		return hostload.Reading{}, hostload.ErrUnknown
	}
	r, err := f.readings[0], f.errs[0]
	f.readings, f.errs = f.readings[1:], f.errs[1:]
	return r, err
}

func reading(busy, total, avail uint64) hostload.Reading {
	return hostload.Reading{CPUBusy: busy, CPUTotal: total, MemoryTotal: 1000, MemoryAvailable: avail}
}

// A live tick's host CPU is the change since the last tick, a stored tick's since the
// last stored tick; none without a previous reading, and a failed read starts over.
func TestReadHost(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	fh := &fakeHost{
		readings: []hostload.Reading{
			reading(100, 1000, 900), // stored: the first, nothing to compare
			reading(200, 1400, 600), // live: 100 of 400
			reading(500, 1800, 500), // stored: 400 of 800 since the stored one; live 300 of 400
			{},                      // fails
			reading(600, 2000, 400), // stored: starts over
			reading(700, 2200, 400), // live: 100 of 200
		},
		errs: []error{nil, nil, nil, errors.New("tunnel closed"), nil, nil},
	}
	mt.svc.host = fh.read
	host := func(cpu float64, used uint64) *Host {
		return &Host{CPUPercent: cpu, MemoryUsage: used, MemoryTotal: 1000}
	}
	tests := []struct {
		save         bool
		live, stored *Host
	}{
		{true, nil, nil},
		{false, host(25, 400), nil},
		{true, host(75, 500), host(50, 500)},
		{true, nil, nil},
		{true, nil, nil},
		{false, host(50, 600), nil},
		{false, nil, nil}, // no more readings: unknown
	}
	same := func(a, b *Host) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	var prev hostReadings
	for i, tt := range tests {
		live, stored := mt.svc.readHost(t.Context(), environments.Environment{ID: 1}, &prev, tt.save)
		if !same(live, tt.live) || !same(stored, tt.stored) {
			t.Errorf("tick %d: live %+v stored %+v, want %+v %+v", i, live, stored, tt.live, tt.stored)
		}
	}
	mt.svc.host = nil
	if live, stored := mt.svc.readHost(t.Context(), environments.Environment{ID: 1}, &prev, true); live != nil || stored != nil {
		t.Errorf("without a reader: host = %+v %+v", live, stored)
	}
}

// A stored tick keeps the host's load; the summary and the history of every container
// carry it, a user's don't, and hours average only the samples that had it.
func TestHostStored(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	fh := &fakeHost{readings: []hostload.Reading{reading(100, 1000, 900), reading(200, 1400, 600)}, errs: []error{nil, nil}}
	mt.svc.host = fh.read
	ctx := t.Context()
	env := environments.Environment{ID: 1}
	var prev hostReadings
	mt.clock.set(t0.Add(time.Minute))
	if err := mt.svc.collect(ctx, env, true, &prev); err != nil {
		t.Fatal(err)
	}
	mt.clock.set(t0.Add(3 * time.Minute))
	if err := mt.svc.collect(ctx, env, true, &prev); err != nil {
		t.Fatal(err)
	}
	if n := mt.count(t, `SELECT COUNT(*) FROM environment_samples WHERE host_cpu IS NULL`); n != 1 {
		t.Errorf("ticks without the host = %d, want the first", n)
	}
	sum, ok, err := mt.svc.Latest(ctx, 1, Scope{All: true})
	if err != nil || !ok {
		t.Fatalf("Latest: %v %v", ok, err)
	}
	if want := (Host{CPUPercent: 25, MemoryUsage: 400, MemoryTotal: 1000}); sum.Host == nil || *sum.Host != want {
		t.Errorf("summary host = %+v, want %+v", sum.Host, want)
	}
	if sum, _, _ := mt.svc.Latest(ctx, 1, Scope{Owner: "bob"}); sum.Host != nil {
		t.Errorf("a user's summary has the host: %+v", sum.Host)
	}

	q := Query{Env: 1, Scope: Scope{All: true}, Since: t0.Add(-time.Hour), Step: 10 * time.Minute}
	h, err := mt.svc.History(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Points) != 1 || h.Points[0].Host == nil || h.Points[0].Host.CPUPercent != 25 || h.Points[0].Host.MemoryUsage != 400 {
		t.Errorf("raw history = %+v, want the one host sample averaged alone", h.Points)
	}
	q.Scope = Scope{Owner: "bob"}
	if h, _ := mt.svc.History(ctx, q); len(h.Points) == 0 || h.Points[0].Host != nil {
		t.Errorf("a user's history = %+v, want no host", h.Points)
	}

	if err := mt.svc.Cleanup(ctx, t0.Add(2*time.Hour), []int64{1}); err != nil {
		t.Fatal(err)
	}
	q = Query{Env: 1, Scope: Scope{All: true}, Since: t0.Add(-time.Hour), Step: time.Hour}
	h, err = mt.svc.History(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if h.Source != SourceHourly || len(h.Points) == 0 || h.Points[0].Host == nil || h.Points[0].Host.CPUPercent != 25 || h.Points[0].Host.MemoryTotal != 1000 {
		t.Errorf("hourly history = %s %+v, want the host averaged over its samples", h.Source, h.Points)
	}
}
