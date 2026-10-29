package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/metrics"
)

type activityAnswer struct {
	Days []metrics.ActivityDay `json:"days"`
}

// An admin sees every container's events per hour, a user their own; days and tz are
// checked.
func TestEnvironmentActivity(t *testing.T) {
	t.Parallel()
	db, err := metrics.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ot := newOwnTestOpts(t, func(o *Options) {
		o.Activity = metrics.NewActivity(metrics.ActivityOptions{DB: db, Clock: fixedClock{now: metricsNow}})
	})
	for _, r := range []struct {
		env   int64
		hour  time.Time
		owner string
		n     int
	}{
		{ot.local, metricsNow, ot.bobKey, 2},
		{ot.local, metricsNow, ot.alice, 3},
		{ot.local, metricsNow.Add(-time.Hour), "", 4},
		{ot.other, metricsNow, ot.bobKey, 9},
	} {
		if _, err := db.Exec(`INSERT INTO activity_hours (environment_id, hour, owner, count) VALUES (?, ?, ?, ?)`,
			r.env, r.hour.Unix(), r.owner, r.n); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name   string
		query  string
		cookie *http.Cookie
		days   int
		// want are the last day's counts by hour.
		want map[int]int64
	}{
		{"admin", "", ot.admin, 7, map[int]int64{11: 4, 12: 5}},
		{"bob", "", ot.bob, 7, map[int]int64{12: 2}},
		{"one day in Warsaw", "?days=1&tz=Europe/Warsaw", ot.admin, 1, map[int]int64{12: 4, 13: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getJSON[activityAnswer](t, ot.h, ot.path(ot.local, "/activity"+tt.query), tt.cookie)
			if len(got.Days) != tt.days {
				t.Fatalf("%d days, want %d", len(got.Days), tt.days)
			}
			last := got.Days[len(got.Days)-1]
			if last.Date != "2026-10-27" {
				t.Errorf("last day %s", last.Date)
			}
			hours := map[int]int64{}
			for h, n := range last.Counts {
				if n != 0 {
					hours[h] = n
				}
			}
			if len(hours) != len(tt.want) {
				t.Errorf("hours = %v, want %v", hours, tt.want)
			}
			for h, n := range tt.want {
				if hours[h] != n {
					t.Errorf("hours = %v, want %v", hours, tt.want)
				}
			}
		})
	}
	for _, q := range []string{"?days=0", "?days=31", "?days=x", "?tz=Mars/Base", "?tz=../../etc/passwd", "?tz=Local"} {
		if rec := do(t, ot.h, request{method: "GET", path: ot.path(ot.local, "/activity"+q), cookie: ot.admin}); rec.Code != http.StatusBadRequest {
			t.Errorf("GET activity%s = %d, want 400", q, rec.Code)
		}
	}

	off := newOwnTest(t)
	if rec := do(t, off.h, request{method: "GET", path: off.path(off.local, "/activity"), cookie: off.admin}); rec.Code != http.StatusNotFound {
		t.Errorf("without an activity counter = %d, want 404", rec.Code)
	}
}

// The ownership rule the activity counter files events under.
func TestEventOwner(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	labelled := map[string]string{ownerLabel: "anything"}
	tests := []struct {
		id    string
		attrs map[string]string
		want  string
	}{
		{"mine", labelled, ot.bobKey},
		{"alices", labelled, ot.alice},
		{"forged", labelled, ""},
		{"admins", labelled, ""},
		{"mine", nil, ""},
		{"gone", labelled, ""},
	}
	for _, tt := range tests {
		got, err := EventOwner(t.Context(), ot.fake, tt.id, tt.attrs)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("EventOwner(%s, %v) = %q, want %q", tt.id, tt.attrs, got, tt.want)
		}
	}
}
