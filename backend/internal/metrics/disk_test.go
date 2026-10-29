package metrics

import (
	"testing"
	"time"
)

// SaveDisk replaces the last measurement; Cleanup drops a removed environment's.
func TestDisk(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	ctx := t.Context()
	if _, found, err := mt.svc.Disk(ctx, 1); err != nil || found {
		t.Fatalf("Disk before a measurement = found %v, %v; want not found", found, err)
	}
	first := Disk{
		MeasuredAt: t0,
		Volumes:    DiskPart{Size: 100, Reclaimable: 10, Count: 3, Active: 2},
		Images:     DiskPart{Size: 200, Reclaimable: 20, Count: 4, Active: 1},
		BuildCache: DiskPart{Size: 30, Reclaimable: 30, Count: 5},
		Containers: DiskPart{Size: 5, Count: 2, Active: 2},
	}
	second := first
	second.MeasuredAt = t0.Add(time.Hour)
	second.Images.Size = 250
	for _, env := range []int64{1, 2} {
		if err := mt.svc.SaveDisk(ctx, env, first); err != nil {
			t.Fatal(err)
		}
	}
	if err := mt.svc.SaveDisk(ctx, 1, second); err != nil {
		t.Fatal(err)
	}
	got, found, err := mt.svc.Disk(ctx, 1)
	if err != nil || !found {
		t.Fatalf("Disk = found %v, %v", found, err)
	}
	if !got.MeasuredAt.Equal(second.MeasuredAt) || got.Volumes != second.Volumes || got.Images != second.Images ||
		got.BuildCache != second.BuildCache || got.Containers != second.Containers {
		t.Errorf("Disk = %+v, want %+v", got, second)
	}

	if err := mt.svc.Cleanup(ctx, t0.Add(time.Hour), []int64{1}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := mt.svc.Disk(ctx, 2); err != nil || found {
		t.Errorf("Disk of a removed environment = found %v, %v; want dropped", found, err)
	}
	if _, found, _ := mt.svc.Disk(ctx, 1); !found {
		t.Error("Cleanup dropped a kept environment's disk usage")
	}
}
