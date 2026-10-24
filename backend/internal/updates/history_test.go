package updates

import (
	"context"
	"testing"
	"time"
)

func TestHistory(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t, nil)
	ctx := context.Background()
	at := start
	web := Change{Environment: 1, Kind: KindContainer, Target: "web", Image: "nginx:1.29",
		OldImageID: "sha256:o", OldDigest: "sha256:od", NewImageID: "sha256:n", NewDigest: "sha256:nd", User: "admin"}
	app := Change{Environment: 1, Kind: KindStack, Target: "app", Service: "db", Image: "postgres:17", User: "admin"}
	other := web
	other.Environment = 2
	if err := s.Record(ctx, []Change{web, app, other}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, nil); err != nil {
		t.Fatalf("Record(nil) = %v", err)
	}

	tests := []struct {
		name   string
		kind   Kind
		target string
		want   []string
	}{
		{"all", "", "", []string{"app", "web"}},
		{"one container", KindContainer, "web", []string{"web"}},
		{"kind must match", KindStack, "web", nil},
		{"unknown", KindContainer, "nope", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.History(ctx, 1, tt.kind, tt.target)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("History = %+v, want targets %v", got, tt.want)
			}
			for i, c := range got {
				if c.Target != tt.want[i] {
					t.Errorf("[%d].Target = %q, want %q", i, c.Target, tt.want[i])
				}
			}
		})
	}

	got, err := s.History(ctx, 1, KindContainer, "web")
	if err != nil || len(got) != 1 {
		t.Fatalf("History = %+v, %v", got, err)
	}
	web.ID, web.Time = got[0].ID, at
	if got[0] != web {
		t.Errorf("History()[0] = %+v, want %+v", got[0], web)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	t.Parallel()
	s, _, clock := newTestService(t, nil)
	ctx := context.Background()
	for i := range HistoryLimit + 5 {
		clock.advance(time.Second)
		// A stack update of three services, and one of another stack.
		var changes []Change
		for _, svc := range []string{"app", "db", "cache"} {
			changes = append(changes, Change{Environment: 1, Kind: KindStack, Target: "web", Service: svc, NewImageID: string(rune('a' + i))})
		}
		changes = append(changes, Change{Environment: 1, Kind: KindStack, Target: "other"})
		if err := s.Record(ctx, changes); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.History(ctx, 1, KindStack, "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3*HistoryLimit || got[0].NewImageID != string(rune('a'+HistoryLimit+4)) {
		t.Errorf("History kept %d rows, newest %q; want %d updates of 3 services, newest last recorded", len(got), got[0].NewImageID, HistoryLimit)
	}
	if other, _ := s.History(ctx, 1, KindStack, "other"); len(other) != HistoryLimit {
		t.Errorf("other kept %d, want %d", len(other), HistoryLimit)
	}
}

func TestDigestFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ref     string
		digests []string
		want    string
	}{
		{"nginx:1.29", []string{"nginx@sha256:a"}, "sha256:a"},
		{"docker.io/library/nginx", []string{"nginx@sha256:a"}, "sha256:a"},
		{"ghcr.io/o/app:v1", []string{"nginx@sha256:a", "ghcr.io/o/app@sha256:b"}, "sha256:b"},
		{"ghcr.io/o/app:v1", []string{"nginx@sha256:a"}, ""},
		{"app:dev", nil, ""},
	}
	for _, tt := range tests {
		if got := DigestFor(tt.ref, tt.digests); got != tt.want {
			t.Errorf("DigestFor(%q, %v) = %q, want %q", tt.ref, tt.digests, got, tt.want)
		}
	}
}
