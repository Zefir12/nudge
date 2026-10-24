package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want bool
	}{
		{"web", true},
		{"my-app_2", true},
		{"0", true},
		{strings.Repeat("a", MaxNameLength), true},
		{strings.Repeat("a", MaxNameLength+1), false},
		{"", false},
		{"Web", false},
		{"-web", false},
		{"_web", false},
		{"../etc", false},
		{"a/b", false},
		{"a.b", false},
		{"a b", false},
	}
	for _, tt := range tests {
		if got := ValidName(tt.name); got != tt.want {
			t.Errorf("ValidName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDir(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "stacks", "1")
	d := NewDir(root)

	names, err := d.List()
	if err != nil || len(names) != 0 {
		t.Fatalf("List before any stack = %v, %v; want none", names, err)
	}
	if _, err := d.Read("web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read missing = %v, want ErrNotFound", err)
	}
	web := Files{Compose: "services: {}\n", Env: "TAG=1\n"}
	if err := d.Create("web", web); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.Create("web", web); !errors.Is(err, ErrExists) {
		t.Fatalf("second Create = %v, want ErrExists", err)
	}
	if err := d.Create("db", Files{Compose: "services: {}\n"}); err != nil {
		t.Fatalf("Create db: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "Not.A.Stack"), 0o700); err != nil {
		t.Fatal(err)
	}
	if names, err := d.List(); err != nil || !reflect.DeepEqual(names, []string{"db", "web"}) {
		t.Fatalf("List = %v, %v; want [db web]", names, err)
	}
	if got, err := d.Read("web"); err != nil || got != web {
		t.Fatalf("Read = %+v, %v; want %+v", got, err, web)
	}

	// An empty Env removes .env.
	if err := d.Update("web", Files{Compose: "services:\n  a: {}\n"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "web", EnvFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".env after an update without env: %v, want gone", err)
	}
	if got, _ := d.Read("web"); got.Compose != "services:\n  a: {}\n" || got.Env != "" {
		t.Errorf("Read after Update = %+v", got)
	}
	if err := d.Update("nope", web); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v, want ErrNotFound", err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "web"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file %s left behind", e.Name())
		}
	}

	if err := d.Remove("web"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := d.Remove("web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Remove = %v, want ErrNotFound", err)
	}
	if names, _ := d.List(); !reflect.DeepEqual(names, []string{"db"}) {
		t.Errorf("List after Remove = %v, want [db]", names)
	}
}

func TestDirRefusesInvalidNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := NewDir(filepath.Join(root, "1"))
	f := Files{Compose: "services: {}\n"}
	for _, name := range []string{"../escape", "", "A"} {
		if err := d.Create(name, f); err == nil {
			t.Errorf("Create(%q) succeeded", name)
		}
		if err := d.Write(name, f); err == nil {
			t.Errorf("Write(%q) succeeded", name)
		}
		if _, err := d.Read(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("Read(%q) = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file was written outside the stacks directory")
	}
}

// recorder is an ExecFunc that records the command and writes the given output.
type recorder struct {
	cmd    Command
	stdout string
	stderr string
	code   int
	err    error
}

func (r *recorder) exec(_ context.Context, cmd Command) (int, error) {
	r.cmd = cmd
	_, _ = cmd.Stdout.Write([]byte(r.stdout))
	_, _ = cmd.Stderr.Write([]byte(r.stderr))
	return r.code, r.err
}

func TestLocalRuns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(l *Local, out func(Line)) (int, error)
		args []string
	}{
		{"up", func(l *Local, out func(Line)) (int, error) { return l.Up(context.Background(), "web", false, out) },
			[]string{"up", "--detach", "--remove-orphans"}},
		{"up with pull", func(l *Local, out func(Line)) (int, error) { return l.Up(context.Background(), "web", true, out) },
			[]string{"up", "--detach", "--remove-orphans", "--pull", "always"}},
		{"down", func(l *Local, out func(Line)) (int, error) { return l.Down(context.Background(), "web", false, out) },
			[]string{"down", "--remove-orphans"}},
		{"down with volumes", func(l *Local, out func(Line)) (int, error) {
			return l.Down(context.Background(), "web", true, out)
		}, []string{"down", "--remove-orphans", "--volumes"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := NewDir(t.TempDir())
			if err := d.Create("web", Files{Compose: "services: {}\n"}); err != nil {
				t.Fatal(err)
			}
			rec := &recorder{stdout: "one\ntwo", stderr: " Container web-a-1  Started\r\n", code: 3}
			l := NewLocal(LocalOptions{Dir: d, Compose: "/bin/compose", DockerHost: "unix:///docker.sock", Exec: rec.exec})
			var lines []Line
			code, err := tt.run(l, func(line Line) { lines = append(lines, line) })
			if err != nil || code != 3 {
				t.Fatalf("run = %d, %v; want exit code 3", code, err)
			}
			dir := d.Path("web")
			wantArgs := append([]string{
				"--project-name", "web", "--project-directory", dir, "--file", dir + "/compose.yaml",
				"--ansi", "never", "--progress", "plain",
			}, tt.args...)
			if rec.cmd.Path != "/bin/compose" || rec.cmd.Dir != dir || !reflect.DeepEqual(rec.cmd.Args, wantArgs) {
				t.Errorf("command = %s %q in %s, want %q in %s", rec.cmd.Path, rec.cmd.Args, rec.cmd.Dir, wantArgs, dir)
			}
			if !slices.Contains(rec.cmd.Env, "DOCKER_HOST=unix:///docker.sock") {
				t.Errorf("env %q has no DOCKER_HOST", rec.cmd.Env)
			}
			for _, kv := range rec.cmd.Env {
				if !strings.HasPrefix(kv, "DOCKER_HOST=") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "PATH=") {
					t.Errorf("env passes on %q", kv)
				}
			}
			slices.SortFunc(lines, func(a, b Line) int { return strings.Compare(a.Stream+a.Text, b.Stream+b.Text) })
			want := []Line{{"stderr", " Container web-a-1  Started"}, {"stdout", "one"}, {"stdout", "two"}}
			if !reflect.DeepEqual(lines, want) {
				t.Errorf("lines = %q, want %q", lines, want)
			}
		})
	}
}

func TestLocalErrors(t *testing.T) {
	t.Parallel()
	d := NewDir(t.TempDir())
	rec := &recorder{err: errors.New("killed")}
	l := NewLocal(LocalOptions{Dir: d, Exec: rec.exec})
	if _, err := l.Up(context.Background(), "web", false, func(Line) {}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Up of a missing stack = %v, want ErrNotFound", err)
	}
	if err := l.Sync(context.Background(), "web", Files{Compose: "services: {}\n"}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := l.Up(context.Background(), "web", false, func(Line) {}); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("Up with a failing exec = %v", err)
	}
	if rec.cmd.Path != DefaultCompose {
		t.Errorf("compose = %s, want the default %s", rec.cmd.Path, DefaultCompose)
	}
	if err := l.Remove(context.Background(), "web"); err != nil {
		t.Errorf("Remove: %v", err)
	}
	if _, err := d.Read("web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read after Remove = %v, want ErrNotFound", err)
	}
}

func TestLineWriter(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", maxLine+10)
	tests := []struct {
		name   string
		writes []string
		want   []string
	}{
		{"lines", []string{"a\nb\n"}, []string{"a", "b"}},
		{"split writes", []string{"he", "llo\nwor", "ld"}, []string{"hello", "world"}},
		{"crlf and blank lines", []string{"a\r\n\n\nb"}, []string{"a", "b"}},
		{"long line", []string{long + "\n"}, []string{long[:maxLine], long[maxLine:]}},
		{"long unterminated", []string{long}, []string{long[:maxLine], long[maxLine:]}},
		{"long line cut between runes", []string{strings.Repeat("x", maxLine-1) + "é\n"}, []string{strings.Repeat("x", maxLine-1), "é"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			w := &lineWriter{stream: "stdout", emit: func(l Line) { got = append(got, l.Text) }}
			for _, s := range tt.writes {
				if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			w.flush()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("lines = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHelperCompose is the fake compose runCommand starts: it isn't a test unless
// NUDGE_FAKE_COMPOSE is set.
func TestHelperCompose(_ *testing.T) {
	switch os.Getenv("NUDGE_FAKE_COMPOSE") {
	case "":
		return
	case "exit":
		fmt.Println("pulling")
		fmt.Fprintln(os.Stderr, "failed")
		os.Exit(3)
	case "signal":
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Kill()
		time.Sleep(time.Minute)
	case "hang":
		fmt.Println("started")
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func TestRunCommand(t *testing.T) {
	t.Parallel()
	helper := func(mode string, out *lineWriter) Command {
		return Command{
			Path: os.Args[0], Args: []string{"-test.run=^TestHelperCompose$"},
			Env: []string{"NUDGE_FAKE_COMPOSE=" + mode}, Dir: t.TempDir(), Stdout: out, Stderr: out,
		}
	}
	var lines []string
	out := &lineWriter{emit: func(l Line) { lines = append(lines, l.Text) }}
	code, err := runCommand(context.Background(), helper("exit", out))
	if err != nil || code != 3 {
		t.Fatalf("runCommand = %d, %v; want exit code 3", code, err)
	}
	if !slices.Contains(lines, "pulling") || !slices.Contains(lines, "failed") {
		t.Errorf("output = %q, want pulling and failed", lines)
	}

	if _, err := runCommand(context.Background(), helper("signal", &lineWriter{emit: func(Line) {}})); err == nil {
		t.Error("runCommand of a killed compose succeeded")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := runCommand(ctx, helper("hang", &lineWriter{emit: func(Line) {}})); err == nil {
		t.Error("runCommand of a cancelled run succeeded")
	}

	missing := Command{Path: filepath.Join(t.TempDir(), "docker-compose")}
	if _, err := runCommand(context.Background(), missing); !errors.Is(err, ErrNoCompose) {
		t.Errorf("runCommand without the binary = %v, want ErrNoCompose", err)
	}
}
