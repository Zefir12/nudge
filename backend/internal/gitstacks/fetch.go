package gitstacks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	// DefaultMaxCloneBytes bounds what one clone may write to disk.
	DefaultMaxCloneBytes = 256 << 20
	// DefaultFetchTimeout bounds one look at a repository, clone included.
	DefaultFetchTimeout = 2 * time.Minute
	// maxFileBytes bounds a compose or env file; an agent takes 2 MB of files per stack.
	maxFileBytes = 1 << 20
	// objectCache bounds the objects go-git keeps in memory while reading a clone.
	objectCache = 32 << 20
	tempPattern = ".git-clone-*"
	// knownHostsPattern names the file known_hosts spends a moment in.
	knownHostsPattern = ".known-hosts-*"
)

// Error is a failed look at a repository. Msg is safe to show: it never quotes the
// remote, whose words can't be trusted; the wrapped error is for the log.
type Error struct {
	Msg string
	Err error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Msg
	}
	return e.Msg + ": " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

var errCloneTooLarge = errors.New("clone too large")

// fetchError names what went wrong in a look at a repository.
func fetchError(err error) *Error {
	var ke *knownhosts.KeyError
	msg := "could not read the repository"
	switch {
	case errors.Is(err, errCloneTooLarge):
		msg = "the repository is too large to clone"
	case errors.Is(err, context.DeadlineExceeded):
		msg = "the repository did not answer in time"
	case errors.Is(err, context.Canceled):
		msg = "the pull was cancelled"
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed),
		err != nil && strings.Contains(err.Error(), "unable to authenticate"):
		msg = "the repository refused the credentials"
	case errors.Is(err, transport.ErrRepositoryNotFound):
		msg = "the repository was not found"
	case errors.Is(err, transport.ErrEmptyRemoteRepository):
		msg = "the repository is empty"
	case errors.As(err, &ke):
		msg = "the server's SSH host key is not in known_hosts"
	}
	return &Error{Msg: msg, Err: err}
}

// Result is what a pull got from a repository.
type Result struct {
	Commit  string
	Compose string
	// Env is the env file's text, empty if the source has none.
	Env string
}

// Head returns the commit src's ref points at, without cloning.
func (s *Service) Head(ctx context.Context, src Source) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	auth, err := s.auth(src)
	if err != nil {
		return "", err
	}
	_, hash, err := resolve(ctx, src, auth)
	return hash, err
}

// Fetch reads src's files at the commit its ref points at, from a shallow clone that is
// removed again. Nothing is checked out: the files are read from the commit, so no path or
// symlink in the repository can touch the disk.
func (s *Service) Fetch(ctx context.Context, src Source) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	auth, err := s.auth(src)
	if err != nil {
		return Result{}, err
	}
	ref, _, err := resolve(ctx, src, auth)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(s.tempDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create stacks directory: %w", err)
	}
	dir, err := os.MkdirTemp(s.tempDir, tempPattern)
	if err != nil {
		return Result{}, fmt.Errorf("create clone directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil { // #nosec G703 -- our own temporary directory
			s.log.Warn("remove clone", "err", err)
		}
	}()
	limit := &limitFS{Filesystem: osfs.New(dir), left: new(atomic.Int64)}
	limit.left.Store(s.maxClone)
	storage := filesystem.NewStorage(limit, cache.NewObjectLRU(objectCache))
	repo, err := git.CloneContext(ctx, storage, nil, &git.CloneOptions{
		URL: src.RepoURL, Auth: auth, ReferenceName: ref, SingleBranch: true, Depth: 1, Tags: git.NoTags, NoCheckout: true,
	})
	if err != nil {
		return Result{}, fetchError(err)
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return Result{}, fetchError(err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return Result{}, fetchError(err)
	}
	res := Result{Commit: hash.String()}
	if res.Compose, err = readFile(commit, src.ComposePath); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(res.Compose) == "" {
		return Result{}, &Error{Msg: "the compose file " + src.ComposePath + " is empty"}
	}
	if src.EnvPath != "" {
		if res.Env, err = readFile(commit, src.EnvPath); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

// readFile reads a regular file of a commit, bounded.
func readFile(commit *object.Commit, name string) (string, error) {
	tree, err := commit.Tree()
	if err != nil {
		return "", fetchError(err)
	}
	entry, err := tree.FindEntry(name)
	if err != nil {
		return "", &Error{Msg: name + " is not in the repository", Err: err}
	}
	if entry.Mode != filemode.Regular && entry.Mode != filemode.Executable && entry.Mode != filemode.Deprecated {
		return "", &Error{Msg: name + " is not a regular file"}
	}
	file, err := tree.TreeEntryFile(entry)
	if err != nil {
		return "", fetchError(err)
	}
	if file.Size > maxFileBytes {
		return "", &Error{Msg: name + " is too large"}
	}
	r, err := file.Reader()
	if err != nil {
		return "", fetchError(err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(io.LimitReader(r, maxFileBytes+1))
	if err != nil {
		return "", fetchError(err)
	}
	if len(b) > maxFileBytes {
		return "", &Error{Msg: name + " is too large"}
	}
	return string(b), nil
}

// resolve asks the remote what src's ref is: its full name and the commit it points at.
func resolve(ctx context.Context, src Source, auth transport.AuthMethod) (plumbing.ReferenceName, string, error) {
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{src.RepoURL}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth, PeelingOption: git.AppendPeeled})
	if err != nil {
		return "", "", fetchError(err)
	}
	byName := make(map[plumbing.ReferenceName]*plumbing.Reference, len(refs))
	for _, r := range refs {
		byName[r.Name()] = r
	}
	lookup := func(name plumbing.ReferenceName) (string, bool) {
		if r, ok := byName[name+"^{}"]; ok { // an annotated tag's commit
			return r.Hash().String(), true
		}
		r, ok := byName[name]
		if !ok || r.Type() != plumbing.HashReference {
			return "", false
		}
		return r.Hash().String(), true
	}
	if src.Ref == "" {
		head, ok := byName[plumbing.HEAD]
		if !ok {
			return "", "", &Error{Msg: "the repository has no default branch"}
		}
		if head.Type() == plumbing.SymbolicReference {
			if hash, ok := lookup(head.Target()); ok {
				return head.Target(), hash, nil
			}
			return "", "", &Error{Msg: "the repository has no default branch"}
		}
		return plumbing.HEAD, head.Hash().String(), nil
	}
	for _, name := range []plumbing.ReferenceName{plumbing.NewBranchReferenceName(src.Ref), plumbing.NewTagReferenceName(src.Ref)} {
		if hash, ok := lookup(name); ok {
			return name, hash, nil
		}
	}
	return "", "", &Error{Msg: "the repository has no branch or tag " + src.Ref}
}

// auth opens src's secret into what go-git logs in with; nil for no login.
func (s *Service) auth(src Source) (transport.AuthMethod, error) {
	if src.Auth == AuthNone {
		return nil, nil
	}
	secret, err := s.box.Open(src.secret, secretPurpose)
	if err != nil {
		return nil, &Error{Msg: "the stored token or key can't be decrypted: enter it again", Err: err}
	}
	if src.Auth == AuthToken {
		user := src.Username
		if user == "" {
			user = "git" // Forgejo, Gitea and GitHub only look at the token
		}
		return &githttp.BasicAuth{Username: user, Password: string(secret)}, nil
	}
	return s.sshAuth(src, secret)
}

// sshLogin is go-git's SSH login with a stored key. Host keys come from the source's
// known_hosts alone; with none accepted, go-git would look in ~/.ssh, which isn't ours.
type sshLogin struct {
	user       string
	signer     gossh.Signer
	hostKey    gossh.HostKeyCallback
	algorithms []string
}

func (l *sshLogin) Name() string   { return "nudge-ssh-key" }
func (l *sshLogin) String() string { return "ssh key for " + l.user }

// ClientConfig is the connection's configuration.
func (l *sshLogin) ClientConfig() (*gossh.ClientConfig, error) {
	return &gossh.ClientConfig{
		User: l.user, Auth: []gossh.AuthMethod{gossh.PublicKeys(l.signer)},
		HostKeyCallback: l.hostKey, HostKeyAlgorithms: l.algorithms,
	}, nil
}

func (s *Service) sshAuth(src Source, key []byte) (transport.AuthMethod, error) {
	signer, err := gossh.ParsePrivateKey(key)
	if err != nil {
		return nil, &Error{Msg: "the stored SSH key can't be read: enter it again", Err: err}
	}
	algorithms, err := knownHostAlgorithms(src.KnownHosts)
	if err != nil {
		return nil, &Error{Msg: "the stored known_hosts can't be read: enter it again", Err: err}
	}
	hostKey, err := s.hostKeyCallback(src.KnownHosts)
	if err != nil {
		return nil, err
	}
	user := src.Username
	if user == "" {
		if ep, err := transport.NewEndpoint(src.RepoURL); err == nil {
			user = ep.User
		}
	}
	if user == "" {
		user = "git"
	}
	return &sshLogin{user: user, signer: signer, hostKey: hostKey, algorithms: algorithms}, nil
}

// hostKeyCallback checks host keys against known, known_hosts lines. The library only
// reads files, so they spend a moment in one.
func (s *Service) hostKeyCallback(known string) (gossh.HostKeyCallback, error) {
	if err := os.MkdirAll(s.tempDir, 0o700); err != nil {
		return nil, fmt.Errorf("create stacks directory: %w", err)
	}
	f, err := os.CreateTemp(s.tempDir, knownHostsPattern)
	if err != nil {
		return nil, fmt.Errorf("write known_hosts: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }() // #nosec G703 -- our own temporary file
	if _, err := f.WriteString(known); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write known_hosts: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write known_hosts: %w", err)
	}
	cb, err := knownhosts.New(f.Name())
	if err != nil {
		return nil, &Error{Msg: "known_hosts can't be read", Err: err}
	}
	return cb, nil
}

// knownHostAlgorithms validates known_hosts lines and returns the host key algorithms
// they allow, so the server offers the kind of key that is listed. There must be at least one.
func knownHostAlgorithms(known string) ([]string, error) {
	var algorithms []string
	seen := map[string]bool{}
	add := func(names ...string) {
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				algorithms = append(algorithms, n)
			}
		}
	}
	rest := []byte(known)
	for {
		marker, _, key, _, next, err := gossh.ParseKnownHosts(rest)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &InputError{Msg: "known_hosts must be lines as ssh-keyscan prints them"}
		}
		rest = next
		if marker == "revoked" {
			continue
		}
		if key.Type() == gossh.KeyAlgoRSA {
			add(gossh.KeyAlgoRSASHA512, gossh.KeyAlgoRSASHA256)
		}
		add(key.Type())
	}
	if len(algorithms) == 0 {
		return nil, &InputError{Msg: "an SSH key needs known_hosts: the server's host key, as ssh-keyscan prints it"}
	}
	return algorithms, nil
}

// limitFS fails writes past a total, which bounds what a clone puts on disk.
type limitFS struct {
	billy.Filesystem
	left *atomic.Int64
}

// Create is the Filesystem's Create, bounded.
func (l *limitFS) Create(name string) (billy.File, error) {
	f, err := l.Filesystem.Create(name)
	return l.wrap(f, err)
}

// OpenFile is the Filesystem's OpenFile, bounded.
func (l *limitFS) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	f, err := l.Filesystem.OpenFile(name, flag, perm)
	return l.wrap(f, err)
}

// TempFile is the Filesystem's TempFile, bounded.
func (l *limitFS) TempFile(dir, prefix string) (billy.File, error) {
	f, err := l.Filesystem.TempFile(dir, prefix)
	return l.wrap(f, err)
}

// Chroot is the Filesystem's Chroot, sharing the bound.
func (l *limitFS) Chroot(p string) (billy.Filesystem, error) {
	fs, err := l.Filesystem.Chroot(filepath.ToSlash(p))
	if err != nil {
		return nil, err
	}
	return &limitFS{Filesystem: fs, left: l.left}, nil
}

func (l *limitFS) wrap(f billy.File, err error) (billy.File, error) {
	if err != nil {
		return nil, err
	}
	return &limitFile{File: f, left: l.left}, nil
}

type limitFile struct {
	billy.File
	left *atomic.Int64
}

// Write is the File's Write, failing once the Filesystem's total is used up.
func (f *limitFile) Write(p []byte) (int, error) {
	if f.left.Add(-int64(len(p))) < 0 {
		return 0, errCloneTooLarge
	}
	return f.File.Write(p)
}
