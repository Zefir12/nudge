package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		stderr string
		code   int
		err    error
		want   []string
	}{
		{"valid", "", 0, nil, []string{}},
		{"invalid", "DRAFT/compose.yaml: yaml: line 2: did not find expected key\n\n", 15, nil,
			[]string{"compose.yaml: yaml: line 2: did not find expected key"}},
		{"silent failure", "", 1, nil, []string{"compose rejected the file (exit code 1)"}},
		{"many errors", strings.Repeat("bad\n", 50), 1, nil, slices.Repeat([]string{"bad"}, maxCheckErrors)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			var cmd Command
			var files Files
			exec := func(_ context.Context, c Command) (int, error) {
				cmd = c
				compose, _ := os.ReadFile(c.Dir + "/" + ComposeFile)
				env, _ := os.ReadFile(c.Dir + "/" + EnvFile)
				files = Files{Compose: string(compose), Env: string(env)}
				_, _ = c.Stderr.Write([]byte(strings.ReplaceAll(tt.stderr, "DRAFT/", c.Dir+"/")))
				return tt.code, tt.err
			}
			c := NewChecker(CheckerOptions{TempDir: tmp, Compose: "/bin/compose", Exec: exec})
			draft := Files{Compose: "services: {}\n", Env: "TAG=1\n"}
			got, err := c.Check(context.Background(), draft)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Check = %q, %v; want %q", got, err, tt.want)
			}
			if files != draft {
				t.Errorf("compose read %+v, want %+v", files, draft)
			}
			wantArgs := []string{"--project-name", "draft", "--project-directory", cmd.Dir, "--file", cmd.Dir + "/compose.yaml",
				"--ansi", "never", "config", "--quiet", "--no-env-resolution", "--no-path-resolution"}
			if cmd.Path != "/bin/compose" || !reflect.DeepEqual(cmd.Args, wantArgs) {
				t.Errorf("command = %s %q, want %q", cmd.Path, cmd.Args, wantArgs)
			}
			for _, kv := range cmd.Env {
				if strings.HasPrefix(kv, "DOCKER_HOST=") {
					t.Errorf("check passes %q: it must not reach a daemon", kv)
				}
			}
			// The draft is gone afterwards.
			if left, _ := os.ReadDir(tmp); len(left) != 0 {
				t.Errorf("left behind %v", left)
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	t.Parallel()
	failing := func(context.Context, Command) (int, error) { return 0, ErrNoCompose }
	c := NewChecker(CheckerOptions{TempDir: t.TempDir(), Exec: failing})
	if _, err := c.Check(context.Background(), Files{Compose: "services: {}\n"}); !errors.Is(err, ErrNoCompose) {
		t.Errorf("Check with no compose = %v, want ErrNoCompose", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c = NewChecker(CheckerOptions{TempDir: filepath.Join(file, "dir"), Exec: failing})
	if _, err := c.Check(context.Background(), Files{Compose: "services: {}\n"}); err == nil {
		t.Error("Check with no temp dir succeeded")
	}
}
