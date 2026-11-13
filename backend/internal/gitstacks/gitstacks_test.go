package gitstacks_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/gitstacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/gitstacks/gitstackstest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

const composeText = "services:\n  app:\n    image: nginx\n"

type fixture struct {
	svc  *gitstacks.Service
	st   *store.Store
	env  int64
	temp string
	now  *time.Time
}

func newFixture(t *testing.T, mods ...func(*gitstacks.Options)) fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	envs := environments.New(environments.Options{DB: st.DB(), Now: time.Now, Connect: func(environments.Environment) (docker.Client, error) {
		return &dockertest.Fake{}, nil
	}})
	t.Cleanup(func() { _ = envs.Close() })
	if _, err := envs.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := envs.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("environments = %v, %v", list, err)
	}
	now := time.Unix(1_700_000_000, 0)
	f := fixture{st: st, env: list[0].ID, temp: t.TempDir(), now: &now}
	o := gitstacks.Options{DB: st.DB(), Box: box, TempDir: f.temp, AllowFile: true, Now: func() time.Time { return *f.now }}
	for _, m := range mods {
		m(&o)
	}
	f.svc = gitstacks.New(o)
	return f
}

func (f fixture) prepare(t *testing.T, in gitstacks.Input) gitstacks.Source {
	t.Helper()
	src, err := f.svc.Prepare(context.Background(), f.env, "web", in)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestFetchRefs(t *testing.T) {
	t.Parallel()
	repo := gitstackstest.New(t)
	first := repo.Commit(map[string]string{"deploy/compose.yaml": composeText, "deploy/env": "A=1\n"})
	repo.Tag("v1", false)
	repo.Tag("v1-annotated", true)
	second := repo.Commit(map[string]string{"deploy/compose.yaml": composeText + "# main\n"})
	repo.Branch("staging")
	third := repo.Commit(map[string]string{"deploy/compose.yaml": composeText + "# staging\n"})
	f := newFixture(t)
	tests := []struct {
		ref, commit, suffix string
	}{
		{"staging", third, "# staging\n"},
		{"v1", first, ""},
		{"v1-annotated", first, ""},
		{"", third, "# staging\n"}, // HEAD follows the checked-out branch
	}
	_ = second
	for _, tt := range tests {
		t.Run("ref "+tt.ref, func(t *testing.T) {
			src := f.prepare(t, gitstacks.Input{RepoURL: repo.URL, Ref: tt.ref, ComposePath: "deploy/compose.yaml", EnvPath: "deploy/env"})
			res, err := f.svc.Fetch(context.Background(), src)
			if err != nil {
				t.Fatal(err)
			}
			if res.Commit != tt.commit || res.Compose != composeText+tt.suffix || res.Env != "A=1\n" {
				t.Fatalf("result = %+v, want commit %s", res, tt.commit)
			}
			head, err := f.svc.Head(context.Background(), src)
			if err != nil || head != tt.commit {
				t.Fatalf("head = %s, %v, want %s", head, err, tt.commit)
			}
		})
	}
	// Clones don't stay behind.
	left, err := filepath.Glob(filepath.Join(f.temp, ".*"))
	if err != nil || len(left) != 0 {
		t.Fatalf("left behind: %v, %v", left, err)
	}
}

func TestFetchRefusals(t *testing.T) {
	t.Parallel()
	repo := gitstackstest.New(t)
	repo.Commit(map[string]string{"compose.yaml": composeText, "empty.yaml": "  \n", "big.yaml": strings.Repeat("x", 1<<20+1)})
	repo.Symlink("link.yaml", "/etc/passwd")
	f := newFixture(t)
	tests := []struct {
		name, ref, path, want string
	}{
		{"no such ref", "nope", "compose.yaml", "no branch or tag nope"},
		{"no such file", "", "other.yaml", "other.yaml is not in the repository"},
		{"a directory", "", "dir", "dir is not in the repository"},
		{"a symlink", "", "link.yaml", "link.yaml is not a regular file"},
		{"an empty file", "", "empty.yaml", "empty.yaml is empty"},
		{"too big", "", "big.yaml", "big.yaml is too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := f.prepare(t, gitstacks.Input{RepoURL: repo.URL, Ref: tt.ref, ComposePath: tt.path})
			_, err := f.svc.Fetch(context.Background(), src)
			var ge *gitstacks.Error
			if !errors.As(err, &ge) || !strings.Contains(ge.Msg, tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFetchBounds(t *testing.T) {
	t.Parallel()
	repo := gitstackstest.New(t)
	// Small enough to fit a pipe: over file:// go-git waits for git-upload-pack when it
	// gives up, which is only left blocked on a local pipe. TestSSH has a large one.
	random := make([]byte, 20<<10) // doesn't compress
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	repo.Commit(map[string]string{"compose.yaml": composeText, "blob": string(random)})
	t.Run("size", func(t *testing.T) {
		f := newFixture(t, func(o *gitstacks.Options) { o.MaxCloneBytes = 4 << 10 })
		src := f.prepare(t, gitstacks.Input{RepoURL: repo.URL, ComposePath: "compose.yaml"})
		_, err := f.svc.Fetch(context.Background(), src)
		var ge *gitstacks.Error
		if !errors.As(err, &ge) || !strings.Contains(ge.Msg, "too large") {
			t.Fatalf("err = %v", err)
		}
		if left, _ := filepath.Glob(filepath.Join(f.temp, ".*")); len(left) != 0 {
			t.Fatalf("left behind: %v", left)
		}
	})
	t.Run("time", func(t *testing.T) {
		f := newFixture(t, func(o *gitstacks.Options) { o.FetchTimeout = time.Nanosecond })
		src := f.prepare(t, gitstacks.Input{RepoURL: repo.URL, ComposePath: "compose.yaml"})
		_, err := f.svc.Fetch(context.Background(), src)
		var ge *gitstacks.Error
		if !errors.As(err, &ge) || !strings.Contains(ge.Msg, "in time") {
			t.Fatalf("err = %v", err)
		}
	})
}

// Not parallel: it counts what the whole process allocates, and parallel tests wait until
// the others are done.
func TestFetchRefusesACompressionBomb(t *testing.T) {
	const bomb = 32 << 20 // about 32 KiB once compressed: a local pipe holds it, see TestFetchBounds
	repo := gitstackstest.New(t)
	repo.Commit(map[string]string{"compose.yaml": composeText, "bomb": string(make([]byte, bomb))})
	f := newFixture(t, func(o *gitstacks.Options) { o.MaxObjectBytes = 1 << 20 })
	src := f.prepare(t, gitstacks.Input{RepoURL: repo.URL, ComposePath: "compose.yaml"})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := f.svc.Fetch(context.Background(), src)
	runtime.ReadMemStats(&after)
	var ge *gitstacks.Error
	if !errors.As(err, &ge) || !strings.Contains(ge.Msg, "too large to unpack") {
		t.Fatalf("err = %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > bomb/2 {
		t.Fatalf("allocated %d bytes for a blob of %d", allocated, bomb)
	}
	if left, _ := filepath.Glob(filepath.Join(f.temp, ".*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestFetchTakesAnObjectUpToTheBound(t *testing.T) {
	t.Parallel()
	repo := gitstackstest.New(t)
	repo.Commit(map[string]string{"compose.yaml": composeText, "blob": string(make([]byte, 1<<20))})
	f := newFixture(t, func(o *gitstacks.Options) { o.MaxObjectBytes = 1 << 20 })
	src := f.prepare(t, gitstacks.Input{RepoURL: repo.URL, ComposePath: "compose.yaml"})
	res, err := f.svc.Fetch(context.Background(), src)
	if err != nil || res.Compose != composeText {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestPrepareValidates(t *testing.T) {
	t.Parallel()
	secret := "token"
	key := newKey(t)
	known := "git.example.com ssh-ed25519 " + strings.TrimPrefix(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.signer.PublicKey()))), "ssh-ed25519 ") + "\n"
	ok := func(mod func(*gitstacks.Input)) gitstacks.Input {
		in := gitstacks.Input{RepoURL: "https://git.example.com/a/b.git", ComposePath: "compose.yaml"}
		mod(&in)
		return in
	}
	tests := []struct {
		name string
		in   gitstacks.Input
		want string
	}{
		{"fine", ok(func(*gitstacks.Input) {}), ""},
		{"token", ok(func(i *gitstacks.Input) { i.Auth, i.Secret = gitstacks.AuthToken, &secret }), ""},
		{"ssh", gitstacks.Input{RepoURL: "git@git.example.com:a/b.git", ComposePath: "c.yaml", Auth: gitstacks.AuthSSH, Secret: &key.pem, KnownHosts: known}, ""},
		{"ssh url scheme", gitstacks.Input{RepoURL: "ssh://git@git.example.com:2222/a/b.git", ComposePath: "c.yaml", Auth: gitstacks.AuthSSH, Secret: &key.pem, KnownHosts: known}, ""},
		{"empty url", ok(func(i *gitstacks.Input) { i.RepoURL = "" }), "URL is invalid"},
		{"local path", gitstacks.Input{RepoURL: "/etc", ComposePath: "c.yaml"}, ""}, // AllowFile is on here; see below
		{"git protocol", ok(func(i *gitstacks.Input) { i.RepoURL = "git://git.example.com/a.git" }), "must be https"},
		{"password in url", ok(func(i *gitstacks.Input) { i.RepoURL = "https://me:pw@git.example.com/a.git" }), "not in the repository URL"},
		{"user in url", ok(func(i *gitstacks.Input) { i.RepoURL = "https://tok@git.example.com/a.git" }), "not in the repository URL"},
		{"token over http", ok(func(i *gitstacks.Input) {
			i.RepoURL, i.Auth, i.Secret = "http://git.example.com/a.git", gitstacks.AuthToken, &secret
		}), "need an https"},
		{"ssh without key auth", ok(func(i *gitstacks.Input) { i.RepoURL = "git@git.example.com:a/b.git" }), "needs an SSH key"},
		{"key with https", ok(func(i *gitstacks.Input) { i.Auth, i.Secret, i.KnownHosts = gitstacks.AuthSSH, &key.pem, known }), "needs an ssh://"},
		{"no known_hosts", gitstacks.Input{RepoURL: "git@h:a.git", ComposePath: "c", Auth: gitstacks.AuthSSH, Secret: &key.pem}, "needs known_hosts"},
		{"bad known_hosts", gitstacks.Input{RepoURL: "git@h:a.git", ComposePath: "c", Auth: gitstacks.AuthSSH, Secret: &key.pem, KnownHosts: "nonsense"}, "known_hosts"},
		{"bad key", gitstacks.Input{RepoURL: "git@h:a.git", ComposePath: "c", Auth: gitstacks.AuthSSH, Secret: &secret, KnownHosts: known}, "unencrypted private key"},
		{"unknown auth", ok(func(i *gitstacks.Input) { i.Auth = "magic" }), "auth must be"},
		{"secret without auth", ok(func(i *gitstacks.Input) { i.Secret = &secret }), "needs auth"},
		{"token without secret", ok(func(i *gitstacks.Input) { i.Auth = gitstacks.AuthToken }), "enter the token"},
		{"bad ref", ok(func(i *gitstacks.Input) { i.Ref = "a b" }), "branch or tag"},
		{"no compose", ok(func(i *gitstacks.Input) { i.ComposePath = "" }), "compose path"},
		{"escaping compose", ok(func(i *gitstacks.Input) { i.ComposePath = "../x" }), "compose path"},
		{"absolute compose", ok(func(i *gitstacks.Input) { i.ComposePath = "/etc/passwd" }), "compose path"},
		{"unclean compose", ok(func(i *gitstacks.Input) { i.ComposePath = "a/../b" }), "compose path"},
		{"escaping env", ok(func(i *gitstacks.Input) { i.EnvPath = "../x" }), "env path"},
		{"poll too fast", ok(func(i *gitstacks.Input) { i.Poll = time.Second }), "poll interval"},
		{"poll too slow", ok(func(i *gitstacks.Input) { i.Poll = 25 * time.Hour }), "poll interval"},
	}
	f := newFixture(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Prepare(context.Background(), f.env, "web", tt.in)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var ie *gitstacks.InputError
			if !errors.As(err, &ie) || !strings.Contains(ie.Msg, tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	t.Run("no file urls unless allowed", func(t *testing.T) {
		g := newFixture(t, func(o *gitstacks.Options) { o.AllowFile = false })
		for _, u := range []string{"file:///etc", "/etc", "../x"} {
			_, err := g.svc.Prepare(context.Background(), g.env, "web", gitstacks.Input{RepoURL: u, ComposePath: "c"})
			var ie *gitstacks.InputError
			if !errors.As(err, &ie) {
				t.Fatalf("%s: err = %v", u, err)
			}
		}
	})
}

func TestSecretIsSealedAndKeptOnlyForTheSameTarget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	const plain = "s3cret-token-value"
	secret := plain
	in := gitstacks.Input{RepoURL: "https://git.example.com/a.git", ComposePath: "c.yaml", Auth: gitstacks.AuthToken, Username: "bot", Secret: &secret}
	src := f.prepare(t, in)
	if err := f.svc.Save(ctx, src, "abc"); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := f.st.DB().QueryRow(`SELECT secret_enc FROM stack_sources`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 || bytes.Contains(stored, []byte(plain)) {
		t.Fatalf("secret stored as %q", stored)
	}
	in.Secret = nil
	if _, err := f.svc.Prepare(ctx, f.env, "web", in); err != nil {
		t.Fatalf("same target: %v", err)
	}
	for name, mod := range map[string]func(*gitstacks.Input){
		"url":      func(i *gitstacks.Input) { i.RepoURL = "https://evil.example.com/a.git" },
		"username": func(i *gitstacks.Input) { i.Username = "other" },
	} {
		changed := in
		mod(&changed)
		_, err := f.svc.Prepare(ctx, f.env, "web", changed)
		var ie *gitstacks.InputError
		if !errors.As(err, &ie) {
			t.Fatalf("%s changed: err = %v", name, err)
		}
	}
}

func TestPoll(t *testing.T) {
	t.Parallel()
	repo := gitstackstest.New(t)
	first := repo.Commit(map[string]string{"compose.yaml": composeText})
	f := newFixture(t)
	ctx := context.Background()
	src := f.prepare(t, gitstacks.Input{RepoURL: repo.URL, ComposePath: "compose.yaml", Poll: time.Minute})
	if err := f.svc.Save(ctx, src, first); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var deployed []string
	f.svc.StartPolling(func(_ context.Context, s gitstacks.Source) {
		mu.Lock()
		defer mu.Unlock()
		deployed = append(deployed, s.Stack)
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(deployed)
	}
	f.svc.PollOnce(ctx)
	if count() != 0 {
		t.Fatal("polled before the interval passed")
	}
	*f.now = f.now.Add(time.Minute)
	f.svc.PollOnce(ctx)
	if count() != 0 {
		t.Fatal("deployed a commit it has")
	}
	second := repo.Commit(map[string]string{"compose.yaml": composeText + "# new\n"})
	f.svc.PollOnce(ctx)
	if count() != 0 {
		t.Fatal("polled again before the interval passed")
	}
	*f.now = f.now.Add(time.Minute)
	f.svc.PollOnce(ctx)
	if count() != 1 {
		t.Fatalf("deploys = %d, want 1", count())
	}
	// A commit whose deploy failed isn't tried again.
	if err := f.svc.RecordFailure(ctx, f.env, "web", second, "the deploy failed"); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(time.Minute)
	f.svc.PollOnce(ctx)
	if count() != 1 {
		t.Fatalf("deploys = %d, retried a failed commit", count())
	}
	got, err := f.svc.Get(ctx, f.env, "web")
	if err != nil || got.Commit != first || got.SeenCommit != second || got.LastError == "" {
		t.Fatalf("source = %+v, %v", got, err)
	}
	// A repository that went away is reported, and not deployed.
	if err := os.RemoveAll(repo.Dir); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(time.Minute)
	f.svc.PollOnce(ctx)
	got, _ = f.svc.Get(ctx, f.env, "web")
	if count() != 1 || got.LastError == "the deploy failed" || got.LastError == "" {
		t.Fatalf("deploys = %d, last error %q", count(), got.LastError)
	}
}

type testKey struct {
	signer ssh.Signer
	pem    string
}

func newKey(t *testing.T) testKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{signer: signer, pem: string(pem.EncodeToMemory(block))}
}

// sshGit is a git server over SSH on loopback: a real handshake, with git-upload-pack.
type sshGit struct {
	addr string
	host testKey
}

func startSSHGit(t *testing.T, client testKey) sshGit {
	t.Helper()
	host := newKey(t)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), client.signer.PublicKey().Marshal()) {
			return nil, errors.New("unknown key")
		}
		return &ssh.Permissions{}, nil
	}}
	cfg.AddHostKey(host.signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0") // #nosec G102 -- loopback in a test
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(conn, cfg)
		}
	}()
	return sshGit{addr: ln.Addr().String(), host: host}
}

func serveSSH(conn net.Conn, cfg *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		c, requests, err := ch.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &payload)
				_ = req.Reply(true, nil)
				status := uploadPack(c, payload.Command)
				_, _ = c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				return
			}
		}()
	}
}

// uploadPack answers `git-upload-pack '/path'` on c like a git server.
func uploadPack(c ssh.Channel, command string) uint32 {
	repoPath := strings.Trim(strings.TrimPrefix(command, "git-upload-pack "), "'")
	cmd := exec.Command("git-upload-pack", repoPath) // #nosec G204 -- a test's own repository
	cmd.Stdin, cmd.Stdout = c, c
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return 1
	}
	return 0
}

func TestSSH(t *testing.T) {
	t.Parallel()
	repo := gitstackstest.New(t)
	large := make([]byte, 4<<20) // doesn't compress
	if _, err := rand.Read(large); err != nil {
		t.Fatal(err)
	}
	commit := repo.Commit(map[string]string{"compose.yaml": composeText, "blob": string(large)})
	client := newKey(t)
	srv := startSSHGit(t, client)
	_, port, err := net.SplitHostPort(srv.addr)
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("ssh://git@127.0.0.1:%s%s", port, repo.Dir)
	knownLine := func(k testKey) string {
		return fmt.Sprintf("[127.0.0.1]:%s %s", port, ssh.MarshalAuthorizedKey(k.signer.PublicKey()))
	}
	other := newKey(t)
	tests := []struct {
		name  string
		key   string
		known string
		want  string
	}{
		{"logs in and reads", client.pem, knownLine(srv.host), ""},
		{"host key not listed", client.pem, knownLine(other), "known_hosts"},
		{"key refused", other.pem, knownLine(srv.host), "refused the credentials"},
	}
	t.Run("gives up on a large clone", func(t *testing.T) {
		f := newFixture(t, func(o *gitstacks.Options) { o.MaxCloneBytes = 64 << 10 })
		secret := client.pem
		src := f.prepare(t, gitstacks.Input{RepoURL: url, ComposePath: "compose.yaml", Auth: gitstacks.AuthSSH, Secret: &secret, KnownHosts: knownLine(srv.host)})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := f.svc.Fetch(ctx, src)
		var ge *gitstacks.Error
		if !errors.As(err, &ge) || !strings.Contains(ge.Msg, "too large") || ctx.Err() != nil {
			t.Fatalf("err = %v, context %v", err, ctx.Err())
		}
		if left, _ := filepath.Glob(filepath.Join(f.temp, ".*")); len(left) != 0 {
			t.Fatalf("left behind: %v", left)
		}
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			secret := tt.key
			src := f.prepare(t, gitstacks.Input{RepoURL: url, ComposePath: "compose.yaml", Auth: gitstacks.AuthSSH, Secret: &secret, KnownHosts: tt.known})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res, err := f.svc.Fetch(ctx, src)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if res.Commit != commit || res.Compose != composeText {
					t.Fatalf("result = %+v", res)
				}
				return
			}
			var ge *gitstacks.Error
			if !errors.As(err, &ge) || !strings.Contains(ge.Msg, tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEmptySSHSecretKeepsTheStoredKey(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	key := newKey(t)
	known := "git.example.com " + string(ssh.MarshalAuthorizedKey(newKey(t).signer.PublicKey()))
	in := gitstacks.Input{RepoURL: "git@git.example.com:a.git", ComposePath: "c", Auth: gitstacks.AuthSSH, Secret: &key.pem, KnownHosts: known}
	if err := f.svc.Save(ctx, f.prepare(t, in), "abc"); err != nil {
		t.Fatal(err)
	}
	empty := ""
	in.Secret = &empty
	if _, err := f.svc.Prepare(ctx, f.env, "web", in); err != nil {
		t.Fatal(err)
	}
}

func TestNewSweepsLeftoverClones(t *testing.T) {
	t.Parallel()
	var opts gitstacks.Options
	f := newFixture(t, func(o *gitstacks.Options) { opts = *o })
	clone := filepath.Join(f.temp, ".git-clone-123")
	if err := os.MkdirAll(filepath.Join(clone, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(f.temp, "1")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	gitstacks.New(opts)
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("clone left: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("an environment's directory went: %v", err)
	}
}
