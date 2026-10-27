package stacks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
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
		{"up", func(l *Local, out func(Line)) (int, error) {
			return l.Up(context.Background(), "web", UpOptions{}, out)
		},
			[]string{"up", "--detach", "--remove-orphans"}},
		{"up with pull", func(l *Local, out func(Line)) (int, error) {
			return l.Up(context.Background(), "web", UpOptions{Pull: true}, out)
		},
			[]string{"up", "--detach", "--remove-orphans", "--pull", "always"}},
		{"up with wait", func(l *Local, out func(Line)) (int, error) {
			return l.Up(context.Background(), "web", UpOptions{Pull: true, Wait: 90 * time.Second}, out)
		}, []string{"up", "--detach", "--remove-orphans", "--pull", "always", "--wait", "--wait-timeout", "90"}},
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

// configSeen is what an ExecFunc saw of compose's DOCKER_CONFIG.
type configSeen struct {
	dir     string
	dirMode os.FileMode
	mode    os.FileMode
	config  string
}

// configRun records the configSeen of a run that may be on another goroutine.
type configRun struct {
	mu   sync.Mutex
	seen configSeen
}

func (c *configRun) get() configSeen {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// seeConfig returns an ExecFunc that records DOCKER_CONFIG into run and prints out.
func seeConfig(t *testing.T, run *configRun, out string) ExecFunc {
	return func(_ context.Context, cmd Command) (int, error) {
		var seen configSeen
		defer func() {
			run.mu.Lock()
			run.seen = seen
			run.mu.Unlock()
		}()
		for _, kv := range cmd.Env {
			if v, ok := strings.CutPrefix(kv, "DOCKER_CONFIG="); ok {
				seen.dir = v
			}
		}
		if seen.dir != "" {
			dirInfo, err := os.Stat(seen.dir)
			if err != nil {
				t.Errorf("DOCKER_CONFIG: %v", err)
				return 1, nil
			}
			seen.dirMode = dirInfo.Mode().Perm()
			path := filepath.Join(seen.dir, "config.json")
			info, err := os.Stat(path)
			if err != nil {
				t.Errorf("config.json: %v", err)
				return 1, nil
			}
			seen.mode = info.Mode().Perm()
			b, err := os.ReadFile(path) // #nosec G304 -- a test's temporary file
			if err != nil {
				t.Errorf("config.json: %v", err)
			}
			seen.config = string(b)
		}
		_, _ = cmd.Stdout.Write([]byte(out))
		return 0, nil
	}
}

func TestLocalCredentials(t *testing.T) {
	t.Parallel()
	hub := Credential{Address: "docker.io", Username: "hub", Password: "hub-pass"}
	gh := Credential{Address: "ghcr.io", Username: "bot", Password: "gh:pa\"ss"}
	files := Files{Compose: "services:\n  app:\n    image: ghcr.io/org/app\n", Env: "A=1\n"}
	tests := []struct {
		name string
		// creds is what the Credentials option returns; nil means no option.
		creds   []Credential
		credErr error
		// fromCtx are credentials handed over by the control service.
		fromCtx    []Credential
		down       bool
		wantConfig string
		wantErr    bool
	}{
		{name: "none", creds: []Credential{}},
		{name: "no option"},
		{name: "stored", creds: []Credential{hub, gh},
			wantConfig: `{"auths":{"ghcr.io":{"auth":"Ym90OmdoOnBhInNz"},"https://index.docker.io/v1/":{"auth":"aHViOmh1Yi1wYXNz"}}}`},
		{name: "from the control service", fromCtx: []Credential{gh},
			wantConfig: `{"auths":{"ghcr.io":{"auth":"Ym90OmdoOnBhInNz"}}}`},
		{name: "down gets none", creds: []Credential{hub}, down: true},
		{name: "lookup fails", credErr: errors.New("sealed with another key"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := NewDir(t.TempDir())
			if err := d.Create("web", files); err != nil {
				t.Fatal(err)
			}
			var run configRun
			opts := LocalOptions{Dir: d, Exec: seeConfig(t, &run, "pulled with gh:pa\"ss and hub-pass as Ym90OmdoOnBhInNz\n")}
			called := false
			if tt.creds != nil || tt.credErr != nil {
				opts.Credentials = func(_ context.Context, f Files) ([]Credential, error) {
					called = true
					if f != files {
						t.Errorf("Credentials got %+v, want the stack's files", f)
					}
					return tt.creds, tt.credErr
				}
			}
			l := NewLocal(opts)
			ctx := withCredentials(context.Background(), tt.fromCtx)
			out, lines := collect()
			var err error
			if tt.down {
				_, err = l.Down(ctx, "web", false, out)
			} else {
				_, err = l.Up(ctx, "web", UpOptions{Pull: true}, out)
			}
			if tt.wantErr {
				if err == nil || len(lines()) != 0 {
					t.Errorf("Up = %v with output %q; want an error before compose runs", err, lines())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			seen := run.get()
			if tt.down && called {
				t.Error("down looked up credentials")
			}
			if seen.config != tt.wantConfig {
				t.Errorf("config.json = %s, want %s", seen.config, tt.wantConfig)
			}
			if tt.wantConfig == "" {
				if seen.dir != "" {
					t.Errorf("DOCKER_CONFIG = %s with no credentials", seen.dir)
				}
				return
			}
			if seen.dirMode != 0o700 || seen.mode != 0o600 {
				t.Errorf("modes: dir %o, config.json %o; want 700, 600", seen.dirMode, seen.mode)
			}
			if _, err := os.Stat(seen.dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("DOCKER_CONFIG left behind: %v", err)
			}
			for _, l := range lines() {
				if strings.Contains(l.Text, "pa\"ss") || strings.Contains(l.Text, "Ym90OmdoOnBhInNz") {
					t.Errorf("output line %q has a secret", l.Text)
				}
			}
		})
	}
}

func TestMaskCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		creds []Credential
		in    string
		want  string
	}{
		{"password and auth", []Credential{{Username: "bot", Password: "s3cret"}},
			"s3cret as Ym90OnMzY3JldA==", "******** as ********"},
		{"short password", []Credential{{Username: "u", Password: "a"}},
			"a dTph", "******** ********"},
		// base64("u:dT") is "dTpkVA==": masking the password first would leave "pkVA==".
		{"password inside its auth", []Credential{{Username: "u", Password: "dT"}},
			"auth dTpkVA== and dT", "auth ******** and ********"},
		{"same password twice", []Credential{{Username: "u", Password: "pw"}, {Username: "v", Password: "pw"}},
			"pw", "********"},
		{"no password", []Credential{{Username: "u"}}, "u: dTo=", "u: dTo="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := mask(tt.in, secretsOf(tt.creds)); got != tt.want {
				t.Errorf("mask(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanConfigDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "compose-config")
	if n, err := CleanConfigDir(dir); err != nil || n != 0 {
		t.Fatalf("CleanConfigDir of a missing dir = %d, %v", n, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("created %v, %v; want a 0700 directory", info, err)
	}
	for _, name := range []string{"nudge-compose-1", "nudge-compose-2/sub"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "config.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(dir, "keep")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := CleanConfigDir(dir); err != nil || n != 2 {
		t.Fatalf("CleanConfigDir = %d, %v; want 2 removed", n, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
		t.Errorf("left %v, %v; want only keep", entries, err)
	}
}

func TestLocalConfigDir(t *testing.T) {
	t.Parallel()
	d := NewDir(t.TempDir())
	if err := d.Create("web", Files{Compose: "services: {}\n"}); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(t.TempDir(), "compose-config")
	if _, err := CleanConfigDir(configDir); err != nil {
		t.Fatal(err)
	}
	var run configRun
	l := NewLocal(LocalOptions{Dir: d, ConfigDir: configDir, Exec: seeConfig(t, &run, ""),
		Credentials: func(context.Context, Files) ([]Credential, error) {
			return []Credential{{Address: "ghcr.io", Username: "u", Password: "p"}}, nil
		}})
	if _, err := l.Up(context.Background(), "web", UpOptions{}, func(Line) {}); err != nil {
		t.Fatal(err)
	}
	if seen := run.get(); filepath.Dir(seen.dir) != configDir || !strings.HasPrefix(filepath.Base(seen.dir), "nudge-compose-") {
		t.Errorf("DOCKER_CONFIG = %s, want a nudge-compose-* dir in %s", seen.dir, configDir)
	}
}

func TestCredentialLogValue(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "credential", Credential{Address: "ghcr.io", Username: "bot", Password: "s3cret"})
	if strings.Contains(buf.String(), "s3cret") || !strings.Contains(buf.String(), "ghcr.io") {
		t.Errorf("logged %q", buf.String())
	}
	if s := fmt.Sprint(Credential{Password: "s3cret"}); strings.Contains(s, "s3cret") {
		t.Errorf("printed %q", s)
	}
}

func TestLocalErrors(t *testing.T) {
	t.Parallel()
	d := NewDir(t.TempDir())
	rec := &recorder{err: errors.New("killed")}
	l := NewLocal(LocalOptions{Dir: d, Exec: rec.exec})
	if _, err := l.Up(context.Background(), "web", UpOptions{}, func(Line) {}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Up of a missing stack = %v, want ErrNotFound", err)
	}
	if err := l.Sync(context.Background(), "web", Files{Compose: "services: {}\n"}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := l.Up(context.Background(), "web", UpOptions{}, func(Line) {}); err == nil || !strings.Contains(err.Error(), "killed") {
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
