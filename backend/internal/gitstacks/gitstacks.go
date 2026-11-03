// Package gitstacks keeps where a stack's compose file comes from when that is a git
// repository: the repository, ref and paths, credentials sealed with a secretbox.Box, and
// the commit the stack's files are at. Fetching is in fetch.go, polling in poll.go.
package gitstacks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gossh "golang.org/x/crypto/ssh"

	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
)

// secretPurpose binds sealed tokens and keys to this use of the key.
const secretPurpose = "stack git secret"

const (
	maxURL        = 2048
	maxRef        = 255
	maxPath       = 1024
	maxUsername   = 255
	maxSecret     = 64 * 1024
	maxKnownHosts = 64 * 1024
	// MinPoll is the shortest poll interval; zero is no polling.
	MinPoll = time.Minute
	// MaxPoll is the longest poll interval.
	MaxPoll = 24 * time.Hour
)

// AuthKind is how nudge logs in to a repository.
type AuthKind string

// The ways to log in.
const (
	AuthNone  AuthKind = "none"
	AuthToken AuthKind = "token"
	AuthSSH   AuthKind = "ssh"
)

// ErrNotFound means the stack has no git source.
var ErrNotFound = errors.New("stack has no git source")

// InputError is a validation error whose message can be shown to the user.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Source is a stack's repository, without its secret.
type Source struct {
	Environment int64
	Stack       string
	RepoURL     string
	// Ref is a branch or tag; empty is the repository's default branch.
	Ref         string
	ComposePath string
	// EnvPath, if set, is a file whose variables are merged into the stack's .env.
	EnvPath    string
	Auth       AuthKind
	Username   string
	KnownHosts string
	Poll       time.Duration
	// Commit is the commit of the stack's files, SeenCommit the newest a pull got.
	Commit     string
	SeenCommit string
	LastError  string
	CheckedAt  time.Time
	PulledAt   time.Time

	secret []byte // sealed
}

// Input sets a stack's source. A nil Secret keeps the stored one, if it still goes to the
// same place.
type Input struct {
	RepoURL     string
	Ref         string
	ComposePath string
	EnvPath     string
	Auth        AuthKind
	Username    string
	Secret      *string
	KnownHosts  string
	Poll        time.Duration
}

// Options are the Service's dependencies.
type Options struct {
	DB  *sql.DB
	Box *secretbox.Box
	// TempDir holds the short-lived clones: the stacks directory.
	TempDir string
	// Context stops polling; without one StartPolling does nothing.
	Context context.Context
	Log     *slog.Logger
	// Now is the clock (time.Now by default).
	Now func() time.Time
	// AllowFile accepts file:// repositories: tests only, a user must not read the server's disk.
	AllowFile bool
	// MaxCloneBytes and FetchTimeout override the bounds of a clone (tests).
	MaxCloneBytes int64
	FetchTimeout  time.Duration
}

// Service manages stacks' git sources.
type Service struct {
	db        *sql.DB
	box       *secretbox.Box
	tempDir   string
	ctx       context.Context
	log       *slog.Logger
	now       func() time.Time
	allowFile bool
	maxClone  int64
	timeout   time.Duration
	pollOnce  sync.Once
	// deploy is set once, by StartPolling, before any poll.
	deploy func(ctx context.Context, src Source)
}

// New returns a Service.
func New(o Options) *Service {
	s := &Service{
		db: o.DB, box: o.Box, tempDir: o.TempDir, ctx: o.Context, log: o.Log, now: o.Now,
		allowFile: o.AllowFile, maxClone: o.MaxCloneBytes, timeout: o.FetchTimeout,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.maxClone == 0 {
		s.maxClone = DefaultMaxCloneBytes
	}
	if s.timeout == 0 {
		s.timeout = DefaultFetchTimeout
	}
	s.sweep()
	return s
}

// sweep removes what a crash left in the temporary directory; nothing is using it yet.
func (s *Service) sweep() {
	if s.tempDir == "" {
		return
	}
	for _, pattern := range []string{tempPattern, knownHostsPattern} {
		left, err := filepath.Glob(filepath.Join(s.tempDir, pattern))
		if err != nil {
			s.log.Warn("sweep clones", "err", err)
			continue
		}
		for _, p := range left {
			if err := os.RemoveAll(p); err != nil {
				s.log.Warn("sweep clones", "err", err)
			}
		}
	}
}

// validRepoPath reports whether p names a file inside a repository.
func validRepoPath(p string) bool {
	return p != "" && len(p) <= maxPath && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\\") &&
		!strings.ContainsAny(p, "\x00\n\r") && path.Clean(p) == p && p != "." && !strings.HasPrefix(p, "../")
}

func (s *Service) validate(in Input) error {
	if len(in.RepoURL) > maxURL {
		return &InputError{Msg: "the repository URL is too long"}
	}
	ep, err := transport.NewEndpoint(in.RepoURL)
	if err != nil || in.RepoURL == "" {
		return &InputError{Msg: "the repository URL is invalid"}
	}
	if ep.Password != "" {
		return &InputError{Msg: "put credentials in the token field, not in the repository URL"}
	}
	switch ep.Protocol {
	case "https":
		if in.Auth == AuthSSH {
			return &InputError{Msg: "an SSH key needs an ssh:// or git@host:path URL"}
		}
	case "http":
		if in.Auth != AuthNone {
			return &InputError{Msg: "credentials need an https:// or SSH URL, not http://"}
		}
	case "ssh":
		if in.Auth != AuthSSH {
			return &InputError{Msg: "an SSH URL needs an SSH key"}
		}
	case "file":
		if !s.allowFile || in.Auth != AuthNone {
			return &InputError{Msg: "the repository URL must be https://, http://, ssh:// or git@host:path"}
		}
	default:
		return &InputError{Msg: "the repository URL must be https://, http://, ssh:// or git@host:path"}
	}
	if (ep.Protocol == "https" || ep.Protocol == "http") && ep.User != "" {
		return &InputError{Msg: "put credentials in the token field, not in the repository URL"}
	}
	if ep.Protocol != "file" && ep.Host == "" {
		return &InputError{Msg: "the repository URL has no host"}
	}
	if in.Ref != "" {
		if len(in.Ref) > maxRef || plumbing.NewBranchReferenceName(in.Ref).Validate() != nil {
			return &InputError{Msg: "the ref must be a branch or tag name"}
		}
	}
	if !validRepoPath(in.ComposePath) {
		return &InputError{Msg: "the compose path must be a relative path inside the repository"}
	}
	if in.EnvPath != "" && !validRepoPath(in.EnvPath) {
		return &InputError{Msg: "the env path must be a relative path inside the repository"}
	}
	if len(in.Username) > maxUsername || strings.ContainsAny(in.Username, "\n\r\x00") {
		return &InputError{Msg: "the username is invalid"}
	}
	if in.Poll != 0 && (in.Poll < MinPoll || in.Poll > MaxPoll) {
		return &InputError{Msg: fmt.Sprintf("the poll interval must be 0 (off) or between %s and %s", MinPoll, MaxPoll)}
	}
	switch in.Auth {
	case AuthNone:
		if in.Secret != nil && *in.Secret != "" {
			return &InputError{Msg: "a token or key needs auth \"token\" or \"ssh\""}
		}
	case AuthToken:
	case AuthSSH:
		if len(in.KnownHosts) > maxKnownHosts {
			return &InputError{Msg: "known_hosts is too long"}
		}
		if _, err := knownHostAlgorithms(in.KnownHosts); err != nil {
			return err
		}
	default:
		return &InputError{Msg: "auth must be none, token or ssh"}
	}
	if in.Secret != nil && *in.Secret != "" { // empty keeps the stored one
		if len(*in.Secret) > maxSecret {
			return &InputError{Msg: "the token or key is too long"}
		}
		if in.Auth == AuthSSH {
			if _, err := gossh.ParsePrivateKey([]byte(*in.Secret)); err != nil {
				return &InputError{Msg: "the SSH key must be an unencrypted private key in PEM format"}
			}
		}
	}
	return nil
}

// Prepare validates in as stack's new source, without saving it. The stored secret
// carries over only if the credentials would go to the same place.
func (s *Service) Prepare(ctx context.Context, env int64, stack string, in Input) (Source, error) {
	if in.Auth == "" {
		in.Auth = AuthNone
	}
	if err := s.validate(in); err != nil {
		return Source{}, err
	}
	src := Source{
		Environment: env, Stack: stack, RepoURL: in.RepoURL, Ref: in.Ref, ComposePath: in.ComposePath, EnvPath: in.EnvPath,
		Auth: in.Auth, Username: in.Username, KnownHosts: in.KnownHosts, Poll: in.Poll,
	}
	if in.Auth != AuthSSH {
		src.KnownHosts = ""
	}
	old, err := s.Get(ctx, env, stack)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Source{}, err
	}
	if err == nil {
		src.Commit, src.SeenCommit = old.Commit, old.SeenCommit
	}
	switch {
	case in.Auth == AuthNone:
	case in.Secret != nil && *in.Secret != "":
		src.secret = s.box.Seal([]byte(*in.Secret), secretPurpose)
	case err == nil && len(old.secret) > 0 && old.Auth == in.Auth && old.RepoURL == in.RepoURL && old.Username == in.Username &&
		(in.Auth != AuthSSH || old.KnownHosts == in.KnownHosts):
		src.secret = old.secret
	default:
		return Source{}, &InputError{Msg: "enter the token or key again: it is required when the repository, auth or username changes"}
	}
	return src, nil
}

const sourceColumns = `environment_id, stack, repo_url, ref, compose_path, env_path, auth_kind, username, secret_enc,
	known_hosts, poll_seconds, commit_hash, seen_commit, last_error, checked_at, pulled_at`

type scanner interface{ Scan(dest ...any) error }

func scanSource(row scanner) (Source, error) {
	var src Source
	var poll, checked, pulled int64
	err := row.Scan(&src.Environment, &src.Stack, &src.RepoURL, &src.Ref, &src.ComposePath, &src.EnvPath, &src.Auth, &src.Username,
		&src.secret, &src.KnownHosts, &poll, &src.Commit, &src.SeenCommit, &src.LastError, &checked, &pulled)
	if err != nil {
		return Source{}, err
	}
	src.Poll = time.Duration(poll) * time.Second
	if checked != 0 {
		src.CheckedAt = time.Unix(checked, 0)
	}
	if pulled != 0 {
		src.PulledAt = time.Unix(pulled, 0)
	}
	return src, nil
}

// Get returns a stack's source; ErrNotFound if it has none.
func (s *Service) Get(ctx context.Context, env int64, stack string) (Source, error) {
	src, err := scanSource(s.db.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM stack_sources WHERE environment_id = ? AND stack = ?`, env, stack))
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	if err != nil {
		return Source{}, fmt.Errorf("get stack source: %w", err)
	}
	return src, nil
}

// Save stores src, prepared by Prepare, as a pull of commit: the stack's files must be at it.
func (s *Service) Save(ctx context.Context, src Source, commit string) error {
	now := s.now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO stack_sources (`+sourceColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		src.Environment, src.Stack, src.RepoURL, src.Ref, src.ComposePath, src.EnvPath, string(src.Auth), src.Username, src.secret,
		src.KnownHosts, int64(src.Poll/time.Second), commit, commit, now, now)
	if err != nil {
		return fmt.Errorf("save stack source: %w", err)
	}
	return nil
}

// Remove detaches a stack from its repository; the stack keeps its files.
func (s *Service) Remove(ctx context.Context, env int64, stack string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM stack_sources WHERE environment_id = ? AND stack = ?`, env, stack)
	if err != nil {
		return fmt.Errorf("remove stack source: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordDeployed says the stack's files are at commit and came up.
func (s *Service) RecordDeployed(ctx context.Context, env int64, stack, commit string) error {
	now := s.now().Unix()
	_, err := s.db.ExecContext(ctx, `UPDATE stack_sources SET commit_hash = ?, seen_commit = ?, last_error = '', checked_at = ?, pulled_at = ?
		WHERE environment_id = ? AND stack = ?`, commit, commit, now, now, env, stack)
	if err != nil {
		return fmt.Errorf("record stack commit: %w", err)
	}
	return nil
}

// RecordFailure keeps a failed pull's or deploy's message for the UI. A commit that failed
// to deploy goes in SeenCommit, so polling waits for a newer one.
func (s *Service) RecordFailure(ctx context.Context, env int64, stack, seen, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stack_sources SET seen_commit = CASE WHEN ? = '' THEN seen_commit ELSE ? END, last_error = ?, checked_at = ?
		WHERE environment_id = ? AND stack = ?`, seen, seen, msg, s.now().Unix(), env, stack)
	if err != nil {
		return fmt.Errorf("record stack pull failure: %w", err)
	}
	return nil
}

// Forget removes the source of a removed stack; a stack without one is fine.
func (s *Service) Forget(ctx context.Context, env int64, stack string) error {
	if err := s.Remove(ctx, env, stack); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}
