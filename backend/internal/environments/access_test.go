package environments

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := newTestService(t, &dockertest.Fake{})
	if _, err := s.db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, created_at, updated_at) VALUES (7, 'bob', '', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}
	remote, _, err := s.CreateAgent(ctx, "remote")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	local := envs[0].ID

	check := func(want []int64) {
		t.Helper()
		got, err := s.Access(ctx, 7)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("access = %v, %v, want %v", got, err, want)
		}
		for _, env := range []int64{local, remote.ID} {
			ok, err := s.HasAccess(ctx, 7, env)
			if err != nil || ok != slices.Contains(want, env) {
				t.Errorf("has access to %d = %v, %v (access %v)", env, ok, err, want)
			}
		}
	}
	check([]int64{})
	if err := s.SetAccess(ctx, 7, []int64{remote.ID, local, remote.ID}); err != nil {
		t.Fatal(err)
	}
	check([]int64{local, remote.ID})
	if err := s.SetAccess(ctx, 7, []int64{local, 999}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown environment = %v, want ErrNotFound", err)
	}
	check([]int64{local, remote.ID})
	// Deleting an environment removes its access.
	if err := s.Delete(ctx, remote.ID); err != nil {
		t.Fatal(err)
	}
	check([]int64{local})
	if err := s.SetAccess(ctx, 7, nil); err != nil {
		t.Fatal(err)
	}
	check([]int64{})
}
