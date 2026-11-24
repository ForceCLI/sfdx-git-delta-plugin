// Package gitadapter reads a git repository in-process: revisions, trees,
// file contents and the differences between two revisions.  It does not run
// the git program.
package gitadapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// EmptyTreeID is the object id git gives a tree with no entries.  As a
// revision it stands for a tree that holds no path.
const EmptyTreeID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// InvalidRevisionError reports a revision that names no object.
type InvalidRevisionError struct {
	Context string
}

func (e *InvalidRevisionError) Error() string {
	return e.Context + ": not a valid git revision"
}

// NotACommitError reports a revision that names a tree or a blob.
type NotACommitError struct {
	Label      string
	ObjectType string
}

func (e *NotACommitError) Error() string {
	return fmt.Sprintf("'%s' does not resolve to a commit (it is a %s)", e.Label, e.ObjectType)
}

// RepositoryRefusalError reports a directory that is not a repository this
// program can read.
type RepositoryRefusalError struct {
	Message string
}

func (e *RepositoryRefusalError) Error() string {
	return e.Message
}

// Blob is a file of a revision.
type Blob struct {
	ID   plumbing.Hash
	Mode filemode.FileMode
}

// Revision is the files of one revision.
type Revision struct {
	// Paths are the file paths in the order of the git tree: the entries
	// of each directory sorted by name, a directory sorted as its name
	// followed by a slash.
	Paths []string
	Blobs map[string]Blob
	// entries also holds submodule entries, which are not files.
	entries map[string]Blob
	sorted  []string
}

// ID returns the object id of the file at path.
func (r *Revision) ID(path string) (plumbing.Hash, bool) {
	blob, ok := r.Blobs[path]
	return blob.ID, ok
}

func (r *Revision) sortedPaths() []string {
	if r.sorted == nil {
		r.sorted = append([]string{}, r.Paths...)
		sort.Strings(r.sorted)
	}
	return r.sorted
}

// pathsFor returns the paths that equal a spec or are below one, without
// duplicates.
func (r *Revision) pathsFor(specs []string) []string {
	seen := map[string]bool{}
	var result []string
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	sorted := r.sortedPaths()
	for _, spec := range specs {
		if RootPaths[spec] {
			for _, path := range sorted {
				add(path)
			}
			continue
		}
		if _, ok := r.Blobs[spec]; ok {
			add(spec)
			continue
		}
		prefix := spec + "/"
		for i := sort.SearchStrings(sorted, prefix); i < len(sorted) && strings.HasPrefix(sorted[i], prefix); i++ {
			add(sorted[i])
		}
	}
	return result
}

var emptyRevision = &Revision{Blobs: map[string]Blob{}, entries: map[string]Blob{}}

// Adapter reads one repository.
type Adapter struct {
	repoPath  string
	repo      *git.Repository
	revisions map[string]*Revision
}

// Open opens the repository whose working directory is repoPath.
func Open(repoPath string) (*Adapter, error) {
	absolute, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, err
	}
	repo, err := git.PlainOpenWithOptions(absolute, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, &RepositoryRefusalError{Message: fmt.Sprintf("'%s' is not a git repository", filepath.ToSlash(absolute))}
		}
		return nil, fmt.Errorf("git operation failed: %w", err)
	}
	return &Adapter{repoPath: absolute, repo: repo, revisions: map[string]*Revision{}}, nil
}

// RepoPath returns the absolute path of the working directory.
func (a *Adapter) RepoPath() string {
	return a.repoPath
}

func (a *Adapter) mapError(err error, context string) error {
	var notACommit *NotACommitError
	var invalid *InvalidRevisionError
	var refusal *RepositoryRefusalError
	switch {
	case errors.As(err, &notACommit), errors.As(err, &invalid), errors.As(err, &refusal):
		return err
	case errors.Is(err, plumbing.ErrObjectNotFound), errors.Is(err, plumbing.ErrReferenceNotFound):
		return &InvalidRevisionError{Context: context}
	}
	return fmt.Errorf("git operation failed: %w", err)
}

var hexObjectID = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (a *Adapter) peelRevision(revision string) (*object.Commit, error) {
	// "<revision>:<path>" names a tree or a file of a revision, which is
	// not a commit.
	if colon := strings.Index(revision, ":"); colon > 0 {
		if commit, err := a.peelRevision(revision[:colon]); err == nil {
			if tree, err := commit.Tree(); err == nil {
				path := strings.Trim(revision[colon+1:], "/")
				if path == "" {
					return nil, &NotACommitError{Label: revision, ObjectType: "tree"}
				}
				if entry, err := tree.FindEntry(path); err == nil {
					objectType := "blob"
					if entry.Mode == filemode.Dir {
						objectType = "tree"
					}
					return nil, &NotACommitError{Label: revision, ObjectType: objectType}
				}
			}
		}
		return nil, plumbing.ErrObjectNotFound
	}
	var hash plumbing.Hash
	if hexObjectID.MatchString(revision) {
		hash = plumbing.NewHash(revision)
	} else {
		resolved, err := a.repo.ResolveRevision(plumbing.Revision(revision))
		if err != nil {
			return nil, plumbing.ErrObjectNotFound
		}
		hash = *resolved
	}
	target, err := a.repo.Object(plumbing.AnyObject, hash)
	if err != nil {
		return nil, plumbing.ErrObjectNotFound
	}
	for {
		tag, isTag := target.(*object.Tag)
		if !isTag {
			break
		}
		if target, err = a.repo.Object(plumbing.AnyObject, tag.Target); err != nil {
			return nil, plumbing.ErrObjectNotFound
		}
	}
	switch typed := target.(type) {
	case *object.Commit:
		return typed, nil
	case *object.Tree:
		return nil, &NotACommitError{Label: revision, ObjectType: "tree"}
	default:
		return nil, &NotACommitError{Label: revision, ObjectType: "blob"}
	}
}

// ResolveCommit returns the id of the commit a revision names.  A tag is
// followed to the commit it points to.
func (a *Adapter) ResolveCommit(ref string) (string, error) {
	commit, err := a.peelRevision(ref)
	if err != nil {
		return "", a.mapError(err, ref)
	}
	return commit.Hash.String(), nil
}

// MergeBase returns the best common ancestor of two revisions.  The second
// result is false when they have none.
func (a *Adapter) MergeBase(from, to string) (string, bool, error) {
	context := from + "..." + to
	fromCommit, err := a.peelRevision(from)
	if err != nil {
		return "", false, a.mapError(err, context)
	}
	toCommit, err := a.peelRevision(to)
	if err != nil {
		return "", false, a.mapError(err, context)
	}
	bases, err := fromCommit.MergeBase(toCommit)
	if err != nil {
		return "", false, a.mapError(err, context)
	}
	if len(bases) == 0 {
		return "", false, nil
	}
	return bases[0].Hash.String(), true, nil
}

func isBlobMode(mode filemode.FileMode) bool {
	return mode == filemode.Regular || mode == filemode.Executable || mode == filemode.Symlink
}

func (a *Adapter) flatten(tree *object.Tree) (*Revision, error) {
	revision := &Revision{Blobs: map[string]Blob{}, entries: map[string]Blob{}}
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, entry, err := walker.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if entry.Mode == filemode.Dir {
			continue
		}
		path := strings.ReplaceAll(name, "\\", "/")
		blob := Blob{ID: entry.Hash, Mode: entry.Mode}
		revision.entries[path] = blob
		if isBlobMode(entry.Mode) {
			revision.Blobs[path] = blob
			revision.Paths = append(revision.Paths, path)
		}
	}
	return revision, nil
}

func (a *Adapter) index(revision string) (*Revision, error) {
	if cached, ok := a.revisions[revision]; ok {
		return cached, nil
	}
	if revision == EmptyTreeID {
		return emptyRevision, nil
	}
	commit, err := a.peelRevision(revision)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	flattened, err := a.flatten(tree)
	if err != nil {
		return nil, err
	}
	a.revisions[revision] = flattened
	return flattened, nil
}

// Files returns the files of a revision.
func (a *Adapter) Files(revision string) (*Revision, error) {
	indexed, err := a.index(revision)
	if err != nil {
		return nil, a.mapError(err, revision)
	}
	return indexed, nil
}

// BuildTreeIndex returns an index of the files of a revision that are at
// or below one of the scope paths, or of every file when no scope remains
// after the root paths are removed.  It returns nil when the revision
// cannot be read.
func (a *Adapter) BuildTreeIndex(revision string, scopePaths []string) *TreeIndex {
	indexed, err := a.index(revision)
	if err != nil {
		return nil
	}
	var scopes []string
	for _, path := range scopePaths {
		if !RootPaths[path] {
			scopes = append(scopes, path)
		}
	}
	matches := ScopeMatcher(scopes)
	result := NewTreeIndex()
	for _, path := range indexed.Paths {
		if len(scopes) == 0 || matches(path) {
			result.Add(path)
		}
	}
	return result
}

func (a *Adapter) readBlob(id plumbing.Hash) ([]byte, error) {
	blob, err := a.repo.BlobObject(id)
	if err != nil {
		return nil, err
	}
	reader, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

// RootFileContent returns the content of a file in the top directory of a
// revision.  The second result is false when the revision has no such
// file.
func (a *Adapter) RootFileContent(revision, fileName string) (string, bool, error) {
	commit, err := a.peelRevision(revision)
	if err != nil {
		return "", false, a.mapError(err, revision)
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", false, a.mapError(err, revision)
	}
	for _, entry := range tree.Entries {
		if entry.Name == fileName && isBlobMode(entry.Mode) {
			content, err := a.readBlob(entry.Hash)
			if err != nil {
				return "", false, a.mapError(err, revision)
			}
			return string(content), true, nil
		}
	}
	return "", false, nil
}

var lfsHeader = []byte("version https://git-lfs")

var lfsObjectID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// lfsObjectPath returns the path, relative to the repository, of the object
// a Git LFS pointer names.  The object id is checked to be 64 hexadecimal
// digits, so a pointer cannot name a path outside .git/lfs/objects.
func lfsObjectPath(pointer []byte) (string, error) {
	lines := strings.Split(string(pointer), "\n")
	oid := ""
	if len(lines) > 1 {
		if parts := strings.Split(lines[1], ":"); len(parts) > 1 {
			oid = parts[1]
		}
	}
	if !lfsObjectID.MatchString(oid) {
		return "", fmt.Errorf("Invalid LFS oid")
	}
	return filepath.Join(".git", "lfs", "objects", oid[0:2], oid[2:4], oid), nil
}

// BufferContent returns the content of the file at path in a revision.  A
// Git LFS pointer is replaced by the object it names in .git/lfs/objects.
func (a *Adapter) BufferContent(path, revision string) ([]byte, error) {
	indexed, err := a.index(revision)
	if err != nil {
		return nil, a.mapError(err, revision)
	}
	id, ok := indexed.ID(strings.ReplaceAll(path, "\\", "/"))
	if !ok {
		return nil, fmt.Errorf("git operation failed: Path '%s' not found at '%s'", path, revision)
	}
	content, err := a.readBlob(id)
	if err != nil {
		return nil, a.mapError(err, revision)
	}
	if bytes.HasPrefix(content, lfsHeader) {
		objectPath, err := lfsObjectPath(content)
		if err != nil {
			return nil, fmt.Errorf("git operation failed: %w", err)
		}
		if content, err = os.ReadFile(filepath.Join(a.repoPath, objectPath)); err != nil {
			return nil, fmt.Errorf("git operation failed: %w", err)
		}
	}
	return content, nil
}

// StringContent returns BufferContent as text.
func (a *Adapter) StringContent(path, revision string) (string, error) {
	content, err := a.BufferContent(path, revision)
	return string(content), err
}

func (a *Adapter) grep(pattern string, revision string, candidates func(*Revision) []string) []string {
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return []string{}
	}
	indexed, err := a.index(revision)
	if err != nil {
		return []string{}
	}
	matches := []string{}
	for _, path := range candidates(indexed) {
		content, err := a.readBlob(indexed.Blobs[path].ID)
		if err != nil {
			return []string{}
		}
		if matcher.Match(content) {
			matches = append(matches, path)
		}
	}
	return matches
}

// GrepUnderPaths returns the files at or below the paths whose content
// matches the pattern.  It returns no files when the revision cannot be
// read.
func (a *Adapter) GrepUnderPaths(pattern string, paths []string, revision string) []string {
	return a.grep(pattern, revision, func(indexed *Revision) []string { return indexed.pathsFor(paths) })
}

var (
	leadingDotSlashes = regexp.MustCompile(`^(\./)+`)
	leadingSlashes    = regexp.MustCompile(`^/+`)
)

// PathspecMatcher returns a function that reports whether a path matches
// one of the specs: a spec without *, ? or [ matches itself and the paths
// below it, and another spec matches as a pattern in which * is any text
// and ? is any character.
func PathspecMatcher(specs []string) func(path string) bool {
	var literals []string
	var globs []*regexp.Regexp
	for _, spec := range specs {
		spec = leadingSlashes.ReplaceAllString(leadingDotSlashes.ReplaceAllString(spec, ""), "")
		if !strings.ContainsAny(spec, "*?[") {
			literals = append(literals, spec)
			continue
		}
		var pattern strings.Builder
		pattern.WriteString("^")
		for _, r := range spec {
			switch r {
			case '*':
				pattern.WriteString("(?s:.*)")
			case '?':
				pattern.WriteString("(?s:.)")
			case '.', '+', '^', '$', '{', '}', '(', ')', '|', '\\', ']':
				pattern.WriteString(`\` + string(r))
			default:
				pattern.WriteRune(r)
			}
		}
		pattern.WriteString("$")
		if compiled, err := regexp.Compile(pattern.String()); err == nil {
			globs = append(globs, compiled)
		}
	}
	matchesLiteral := ScopeMatcher(literals)
	return func(path string) bool {
		if len(literals) > 0 && matchesLiteral(path) {
			return true
		}
		for _, glob := range globs {
			if glob.MatchString(path) {
				return true
			}
		}
		return false
	}
}

// GrepMatchingPathspecs returns the files that match one of the pathspecs
// and whose content matches the pattern, in tree order.
func (a *Adapter) GrepMatchingPathspecs(pattern string, specs []string, revision string) []string {
	matches := PathspecMatcher(specs)
	return a.grep(pattern, revision, func(indexed *Revision) []string {
		var candidates []string
		for _, path := range indexed.Paths {
			if matches(path) {
				candidates = append(candidates, path)
			}
		}
		return candidates
	})
}

// ArchivePaths returns the files of a revision at or below path, in tree
// order.
func (a *Adapter) ArchivePaths(path, revision string) ([]string, error) {
	indexed, err := a.index(revision)
	if err != nil {
		return nil, a.mapError(err, revision)
	}
	inScope := ScopeMatcher([]string{path})
	var result []string
	for _, filePath := range indexed.Paths {
		if inScope(filePath) {
			result = append(result, filePath)
		}
	}
	return result, nil
}

// DiffSpec selects the two revisions to compare and how.
type DiffSpec struct {
	From             string
	To               string
	DetectRenames    bool
	IgnoreWhitespace bool
}

// DiffResult is the lines of a diff and how many changes there were before
// the scopes were applied.
type DiffResult struct {
	// Lines are "A\tpath", "M\tpath", "D\tpath" and, with rename
	// detection, "R<score>\tfrom\tto".
	Lines       []string
	ChangesSeen int
}

type change struct {
	kind             string
	oldPath, newPath string
	oldMode, newMode filemode.FileMode
	exact            bool
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

// equalIgnoringWhitespace reports whether two contents have the same lines
// once all white space is removed from each line and lines that are then
// empty are dropped.
func equalIgnoringWhitespace(a, b []byte) bool {
	i, j := 0, 0
	for {
		for i < len(a) && isSpace(a[i]) {
			i++
		}
		for j < len(b) && isSpace(b[j]) {
			j++
		}
		if i == len(a) || j == len(b) {
			return i == len(a) && j == len(b)
		}
		if a[i] != b[j] {
			return false
		}
		i++
		j++
		// A line break between two characters is part of the content: "ab"
		// on one line differs from "a" and "b" on two lines.
		lineBreakA, lineBreakB := false, false
		for i < len(a) && isSpace(a[i]) {
			lineBreakA = lineBreakA || a[i] == '\n'
			i++
		}
		for j < len(b) && isSpace(b[j]) {
			lineBreakB = lineBreakB || b[j] == '\n'
			j++
		}
		if i < len(a) && j < len(b) && lineBreakA != lineBreakB {
			return false
		}
	}
}

func (a *Adapter) changes(spec DiffSpec) ([]change, error) {
	from, err := a.index(spec.From)
	if err != nil {
		return nil, err
	}
	to, err := a.index(spec.To)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for path := range from.entries {
		paths[path] = true
	}
	for path := range to.entries {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	var result []change
	for _, path := range ordered {
		before, inFrom := from.entries[path]
		after, inTo := to.entries[path]
		switch {
		case !inFrom:
			result = append(result, change{kind: "add", newPath: path, newMode: after.Mode})
		case !inTo:
			result = append(result, change{kind: "delete", oldPath: path, oldMode: before.Mode})
		case before.ID == after.ID && before.Mode == after.Mode:
		case (before.Mode == filemode.Symlink) != (after.Mode == filemode.Symlink), (before.Mode == filemode.Submodule) != (after.Mode == filemode.Submodule):
			// A change of the kind of entry is not a modification.
		default:
			if spec.IgnoreWhitespace && before.Mode == after.Mode && isBlobMode(before.Mode) {
				oldContent, err := a.readBlob(before.ID)
				if err != nil {
					return nil, err
				}
				newContent, err := a.readBlob(after.ID)
				if err != nil {
					return nil, err
				}
				if equalIgnoringWhitespace(oldContent, newContent) {
					continue
				}
			}
			result = append(result, change{kind: "modify", oldPath: path, newPath: path, oldMode: before.Mode, newMode: after.Mode})
		}
	}
	if spec.DetectRenames {
		return a.withRenames(spec, result)
	}
	return result, nil
}

// withRenames replaces pairs of a deletion and an addition that git's
// rename detection matches by a rename.
func (a *Adapter) withRenames(spec DiffSpec, changes []change) ([]change, error) {
	if spec.From == EmptyTreeID || spec.To == EmptyTreeID {
		return changes, nil
	}
	fromCommit, err := a.peelRevision(spec.From)
	if err != nil {
		return nil, err
	}
	toCommit, err := a.peelRevision(spec.To)
	if err != nil {
		return nil, err
	}
	fromTree, err := fromCommit.Tree()
	if err != nil {
		return nil, err
	}
	toTree, err := toCommit.Tree()
	if err != nil {
		return nil, err
	}
	options := *object.DefaultDiffTreeOptions
	options.DetectRenames = true
	detected, err := object.DiffTreeWithOptions(context.Background(), fromTree, toTree, &options)
	if err != nil {
		return nil, err
	}
	renamedFrom := map[string]change{}
	renamedTo := map[string]bool{}
	for _, item := range detected {
		if item.From.Name == "" || item.To.Name == "" || item.From.Name == item.To.Name {
			continue
		}
		renamedFrom[item.From.Name] = change{
			kind:    "rename",
			oldPath: item.From.Name,
			newPath: item.To.Name,
			oldMode: item.From.TreeEntry.Mode,
			newMode: item.To.TreeEntry.Mode,
			exact:   item.From.TreeEntry.Hash == item.To.TreeEntry.Hash,
		}
		renamedTo[item.To.Name] = true
	}
	var result []change
	for _, item := range changes {
		switch {
		case item.kind == "delete" && renamedFrom[item.oldPath].kind == "rename":
			result = append(result, renamedFrom[item.oldPath])
		case item.kind == "add" && renamedTo[item.newPath]:
		default:
			result = append(result, item)
		}
	}
	return result, nil
}

// DiffLines compares two revisions and returns the lines for the changes
// that are in one of the scopes.  No scopes means every path.  A change
// with one side in scope and the other outside is reported as the addition
// or the deletion of the side in scope.  Submodule entries are left out.
func (a *Adapter) DiffLines(spec DiffSpec, scopes []string) (*DiffResult, error) {
	changes, err := a.changes(spec)
	if err != nil {
		return nil, a.mapError(err, spec.From+".."+spec.To)
	}
	inScope := ScopeMatcher(scopes)
	keep := func(mode filemode.FileMode, path string) bool {
		return mode != filemode.Submodule && (len(scopes) == 0 || inScope(path))
	}
	result := &DiffResult{ChangesSeen: len(changes)}
	for _, item := range changes {
		switch item.kind {
		case "add":
			if keep(item.newMode, item.newPath) {
				result.Lines = append(result.Lines, "A\t"+item.newPath)
			}
		case "delete":
			if keep(item.oldMode, item.oldPath) {
				result.Lines = append(result.Lines, "D\t"+item.oldPath)
			}
		case "modify":
			if item.oldMode != filemode.Submodule && keep(item.newMode, item.newPath) {
				result.Lines = append(result.Lines, "M\t"+item.newPath)
			}
		case "rename":
			oldKept := keep(item.oldMode, item.oldPath)
			newKept := keep(item.newMode, item.newPath)
			switch {
			case oldKept && newKept:
				score := "R050"
				if item.exact {
					score = "R100"
				}
				result.Lines = append(result.Lines, score+"\t"+item.oldPath+"\t"+item.newPath)
			case newKept:
				result.Lines = append(result.Lines, "A\t"+item.newPath)
			case oldKept:
				result.Lines = append(result.Lines, "D\t"+item.oldPath)
			}
		}
	}
	return result, nil
}
