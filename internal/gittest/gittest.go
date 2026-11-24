// Package gittest builds git repositories for tests without running the
// git program.
package gittest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Repo is a repository in a temporary directory.
type Repo struct {
	Dir  string
	t    *testing.T
	repo *git.Repository
	when time.Time
}

// New creates an empty repository.
func New(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return &Repo{Dir: dir, t: t, repo: repo, when: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Write creates or replaces files, given as path and content pairs.
func (r *Repo) Write(pairs ...string) {
	r.t.Helper()
	for i := 0; i+1 < len(pairs); i += 2 {
		path := filepath.Join(r.Dir, filepath.FromSlash(pairs[i]))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(pairs[i+1]), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

// Remove deletes files or directories.
func (r *Repo) Remove(paths ...string) {
	r.t.Helper()
	for _, path := range paths {
		if err := os.RemoveAll(filepath.Join(r.Dir, filepath.FromSlash(path))); err != nil {
			r.t.Fatal(err)
		}
	}
}

// Move renames a file or directory.
func (r *Repo) Move(from, to string) {
	r.t.Helper()
	target := filepath.Join(r.Dir, filepath.FromSlash(to))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(r.Dir, filepath.FromSlash(from)), target); err != nil {
		r.t.Fatal(err)
	}
}

// Commit records the working directory as a commit and returns its id.
func (r *Repo) Commit(message string) string {
	r.t.Helper()
	worktree, err := r.repo.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		r.t.Fatal(err)
	}
	r.when = r.when.Add(time.Minute)
	signature := &object.Signature{Name: "Test", Email: "test@example.com", When: r.when}
	hash, err := worktree.Commit(message, &git.CommitOptions{Author: signature, Committer: signature, AllowEmptyCommits: true})
	if err != nil {
		r.t.Fatal(err)
	}
	return hash.String()
}

// Tag creates an annotated tag for a commit.
func (r *Repo) Tag(name, commit string) {
	r.t.Helper()
	signature := &object.Signature{Name: "Test", Email: "test@example.com", When: r.when}
	if _, err := r.repo.CreateTag(name, plumbing.NewHash(commit), &git.CreateTagOptions{Tagger: signature, Message: name}); err != nil {
		r.t.Fatal(err)
	}
}

// Branch creates a branch at a commit.
func (r *Repo) Branch(name, commit string) {
	r.t.Helper()
	reference := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), plumbing.NewHash(commit))
	if err := r.repo.Storer.SetReference(reference); err != nil {
		r.t.Fatal(err)
	}
}

// Checkout makes the working directory and HEAD match a commit.
func (r *Repo) Checkout(commit string) {
	r.t.Helper()
	worktree, err := r.repo.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(commit), Force: true}); err != nil {
		r.t.Fatal(err)
	}
}
