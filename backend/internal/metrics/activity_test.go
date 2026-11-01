package metrics

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	dockerevents "github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
)

// onlineEnvs are fakeEnvs whose environments are all online.
type onlineEnvs struct{ *fakeEnvs }

func (onlineEnvs) Online(context.Context, *environments.Environment) bool { return true }

type activityTest struct {
	a      *Activity
	db     *sql.DB
	clock  *fakeClock
	msgs   chan dockerevents.Message
	owners chan string
}

// newActivityTest counts environment 1's events from a fake stream; a container's
// owner is its "owner" attribute, and every lookup sends the container's ID on owners.
func newActivityTest(t *testing.T) activityTest {
	t.Helper()
	db, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	msgs := make(chan dockerevents.Message)
	fake := &dockertest.Fake{EventsResult: client.EventsResult{Messages: msgs, Err: make(chan error)}}
	envs := &fakeEnvs{fakes: map[int64]*dockertest.Fake{1: fake}}
	envs.setList(1)
	clock := &fakeClock{now: t0}
	owners := make(chan string, 100)
	owner := func(_ context.Context, _ docker.Client, id string, attrs map[string]string) (string, error) {
		owners <- id
		return attrs["owner"], nil
	}
	a := NewActivity(ActivityOptions{
		DB: db, Envs: onlineEnvs{envs}, Hub: events.New(events.Options{Context: t.Context()}),
		Owner: owner, Clock: clock,
	})
	return activityTest{a: a, db: db, clock: clock, msgs: msgs, owners: owners}
}

// hours returns the last day's counts of Days(env 1, 1 day, UTC) in sc, by hour.
func (at activityTest) hours(t *testing.T, sc Scope) map[int]int64 {
	t.Helper()
	days, err := at.a.Days(t.Context(), 1, sc, 1, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]int64{}
	for h, n := range days[0].Counts {
		if n != 0 {
			out[h] = n
		}
	}
	return out
}

// Counts add up per hour and owner, stored or still waiting; an admin sees every
// owner's, a user their own, a user without a key none.
func TestActivityScopes(t *testing.T) {
	t.Parallel()
	at := newActivityTest(t)
	rec := func(owner string, t time.Time, n int) {
		for range n {
			at.a.record(1, owner, t)
		}
	}
	rec("bob", t0.Add(-2*time.Hour), 2)
	rec("", t0.Add(-2*time.Hour+59*time.Minute), 3)
	rec("alice", t0.Add(-time.Hour), 1)
	at.a.record(2, "bob", t0) // another environment
	if err := at.a.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	// After the flush: more of the same hour, and the current hour, still waiting.
	rec("bob", t0.Add(-2*time.Hour), 1)
	rec("bob", t0.Add(5*time.Minute), 4)
	tests := []struct {
		name string
		sc   Scope
		want map[int]int64
	}{
		{"admin", Scope{All: true}, map[int]int64{10: 6, 11: 1, 12: 4}},
		{"bob", Scope{Owner: "bob"}, map[int]int64{10: 3, 12: 4}},
		{"alice", Scope{Owner: "alice"}, map[int]int64{11: 1}},
		{"no key", Scope{}, map[int]int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := at.hours(t, tt.sc); !mapsEqual(got, tt.want) {
				t.Errorf("hours = %v, want %v", got, tt.want)
			}
		})
	}
	// A second flush adds to the stored rows.
	if err := at.a.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := at.db.QueryRow(`SELECT count FROM activity_hours WHERE environment_id = 1 AND hour = ? AND owner = 'bob'`,
		t0.Add(-2*time.Hour).Unix()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("stored count = %d, want 3", n)
	}
	if got := at.hours(t, Scope{All: true}); !mapsEqual(got, map[int]int64{10: 6, 11: 1, 12: 4}) {
		t.Errorf("after the second flush hours = %v", got)
	}
}

func mapsEqual(a, b map[int]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Days are local days ending today, oldest first, and an hour goes to the local hour it
// starts in.
func TestActivityDays(t *testing.T) {
	t.Parallel()
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	at := newActivityTest(t)
	// t0 is 2026-10-27 12:00 UTC, a Tuesday; Warsaw is at UTC+1 after the clocks went
	// back on Sunday 25th.
	at.a.record(1, "", t0)                                             // 13:00 in Warsaw
	at.a.record(1, "", time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)) // 25th 01:00 (UTC+2)
	at.a.record(1, "", time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))  // 25th 02:00 (UTC+2)
	at.a.record(1, "", time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC))  // 25th 02:00 again (UTC+1)
	at.a.record(1, "", time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC)) // 21st 01:00: the first day
	at.a.record(1, "", time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)) // 20th: before it
	at.a.record(1, "", time.Date(2026, 10, 27, 23, 0, 0, 0, time.UTC)) // 28th 00:00 (not yet)
	days, err := at.a.Days(t.Context(), 1, Scope{All: true}, 7, warsaw)
	if err != nil {
		t.Fatal(err)
	}
	var dates []string
	for _, d := range days {
		dates = append(dates, d.Date)
		if len(d.Counts) != 24 {
			t.Errorf("%s has %d hours", d.Date, len(d.Counts))
		}
	}
	if want := []string{"2026-10-21", "2026-10-22", "2026-10-23", "2026-10-24", "2026-10-25", "2026-10-26", "2026-10-27"}; !slices.Equal(dates, want) {
		t.Errorf("dates = %v, want %v", dates, want)
	}
	checks := []struct {
		day, hour int
		want      int64
	}{{6, 13, 1}, {4, 1, 1}, {4, 2, 2}, {0, 1, 1}}
	var total int64
	for _, d := range days {
		for _, n := range d.Counts {
			total += n
		}
	}
	for _, c := range checks {
		if got := days[c.day].Counts[c.hour]; got != c.want {
			t.Errorf("%s %02d:00 = %d, want %d", days[c.day].Date, c.hour, got, c.want)
		}
	}
	if total != 5 {
		t.Errorf("total = %d, want 5 (the 20th and the 28th left out)", total)
	}

	// At UTC+5:30 the hour starting 12:00 UTC is 17:30, filed under 17.
	days, err = at.a.Days(t.Context(), 1, Scope{All: true}, 1, kolkata)
	if err != nil {
		t.Fatal(err)
	}
	if days[0].Date != "2026-10-27" || days[0].Counts[17] != 1 {
		t.Errorf("Kolkata: %s 17:00 = %d", days[0].Date, days[0].Counts[17])
	}

	for _, n := range []int{0, MaxActivityDays + 1} {
		if _, err := at.a.Days(t.Context(), 1, Scope{All: true}, n, time.UTC); !errors.Is(err, ErrActivityDays) {
			t.Errorf("days %d: err = %v, want ErrActivityDays", n, err)
		}
	}
}

// Past maxPendingActivity buckets a new owner's count goes to the unowned bucket, so the
// environment's total is still right.
func TestActivityPendingBound(t *testing.T) {
	t.Parallel()
	at := newActivityTest(t)
	for i := range maxPendingActivity {
		at.a.record(1, "", t0.Add(-time.Duration(i+1)*time.Hour))
	}
	at.a.record(1, "bob", t0)
	if got := at.hours(t, Scope{All: true}); got[12] != 1 {
		t.Errorf("admin hours = %v, want 1 at 12", got)
	}
	if got := at.hours(t, Scope{Owner: "bob"}); len(got) != 0 {
		t.Errorf("bob's hours = %v, want none", got)
	}
}

// A failed flush keeps the counts for the next one.
func TestActivityFlushFails(t *testing.T) {
	t.Parallel()
	at := newActivityTest(t)
	at.a.record(1, "", t0)
	if _, err := at.db.Exec(`DROP TABLE activity_hours`); err != nil {
		t.Fatal(err)
	}
	if err := at.a.Flush(t.Context()); err == nil {
		t.Fatal("flush without the table: want an error")
	}
	if n := at.a.pending[activityKey{env: 1, hour: t0.Unix()}]; n != 1 {
		t.Errorf("pending after a failed flush = %d, want 1", n)
	}
}

// Run reads the online environment's events, counts container events but not exec
// ones or other types, looks each container's owner up once, flushes once the hour
// is over, and flushes the rest when it ends.
func TestActivityRun(t *testing.T) {
	t.Parallel()
	at := newActivityTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		at.a.Run(ctx)
	}()
	at.clock.wait(t, 1) // the reader started before the first wait
	send := func(typ, action, id, owner string) {
		at.msgs <- dockerevents.Message{
			Type: dockerevents.Type(typ), Action: dockerevents.Action(action),
			Actor: dockerevents.Actor{ID: id, Attributes: map[string]string{"owner": owner}},
		}
	}
	send("container", "start", "w1", "bob")
	send("container", "exec_start: sh -c true", "w1", "bob")
	send("image", "pull", "nginx", "")
	send("container", "die", "w1", "bob")
	send("container", "start", "d1", "")
	waitFor(t, func() bool { return at.hours(t, Scope{All: true})[12] == 3 })
	if got := at.hours(t, Scope{Owner: "bob"}); !mapsEqual(got, map[int]int64{12: 2}) {
		t.Errorf("bob's hours = %v", got)
	}
	close(at.owners)
	var looked []string
	for id := range at.owners {
		looked = append(looked, id)
	}
	if !slices.Equal(looked, []string{"w1", "d1"}) {
		t.Errorf("owner lookups = %v, want w1 and d1 once", looked)
	}

	stored := func() int64 {
		var n int64
		if err := at.db.QueryRow(`SELECT COALESCE(SUM(count), 0) FROM activity_hours`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := stored(); n != 0 {
		t.Errorf("stored before the hour is over: %d", n)
	}
	at.clock.tick(time.Hour)
	at.clock.wait(t, 2)
	if n := stored(); n != 3 {
		t.Errorf("stored after the hour: %d, want 3", n)
	}
	send("container", "stop", "d1", "")
	waitFor(t, func() bool { return at.hours(t, Scope{All: true})[13] == 1 })
	cancel()
	<-done
	if n := stored(); n != 4 {
		t.Errorf("stored after Run ended: %d, want 4", n)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
