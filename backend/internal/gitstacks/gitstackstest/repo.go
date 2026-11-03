// Package gitstackstest has local bare repositories for tests of git stacks, with no
// network. Reading one over file:// runs git-upload-pack: the tests need git installed,
// which go-git's own server can't replace as it refuses shallow clones.
package gitstackstest

import (
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// Repo is a bare repository in a temp dir with a work tree in memory.
type Repo struct {
	// Dir is the bare repository's path, and URL its file:// URL.
	Dir, URL string
	t        *testing.T
	repo     *git.Repository
	when     time.Time
}

// New returns an empty repository whose default branch is main.
func New(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	storage := filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault())
	repo, err := git.Init(storage, memfs.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatal(err)
	}
	return &Repo{Dir: dir, URL: "file://" + dir, t: t, repo: repo, when: time.Unix(1_700_000_000, 0)}
}

// Commit commits files (path to content) on the current branch and returns the commit.
func (r *Repo) Commit(files map[string]string) string {
	r.t.Helper()
	wt, err := r.repo.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	for name, content := range files {
		f, err := wt.Filesystem.Create(name)
		if err != nil {
			r.t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			r.t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			r.t.Fatal(err)
		}
		if _, err := wt.Add(name); err != nil {
			r.t.Fatal(err)
		}
	}
	r.when = r.when.Add(time.Second)
	hash, err := wt.Commit("change", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            &object.Signature{Name: "test", Email: "test@example.com", When: r.when},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return hash.String()
}

// Symlink commits a symlink at name pointing at target and returns the commit.
func (r *Repo) Symlink(name, target string) string {
	r.t.Helper()
	wt, err := r.repo.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := wt.Filesystem.Symlink(target, name); err != nil {
		r.t.Fatal(err)
	}
	if _, err := wt.Add(name); err != nil {
		r.t.Fatal(err)
	}
	idx, err := r.repo.Storer.Index()
	if err != nil {
		r.t.Fatal(err)
	}
	if e, err := idx.Entry(name); err != nil || e.Mode != filemode.Symlink {
		r.t.Fatalf("symlink entry %v: %v", e, err)
	}
	return r.Commit(nil)
}

// Branch creates a branch at the current commit and switches to it.
func (r *Repo) Branch(name string) {
	r.t.Helper()
	wt, err := r.repo.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name), Create: true, Keep: true}); err != nil {
		r.t.Fatal(err)
	}
}

// Tag creates a lightweight tag at the current commit, or an annotated one.
func (r *Repo) Tag(name string, annotated bool) {
	r.t.Helper()
	head, err := r.repo.Head()
	if err != nil {
		r.t.Fatal(err)
	}
	var opts *git.CreateTagOptions
	if annotated {
		opts = &git.CreateTagOptions{Message: name, Tagger: &object.Signature{Name: "test", Email: "test@example.com", When: r.when}}
	}
	if _, err := r.repo.CreateTag(name, head.Hash(), opts); err != nil {
		r.t.Fatal(err)
	}
}
