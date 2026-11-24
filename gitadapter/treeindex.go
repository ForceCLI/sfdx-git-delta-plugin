package gitadapter

import "strings"

// RootPaths are the paths that stand for the whole repository.
var RootPaths = map[string]bool{"": true, ".": true, "./": true}

type trieNode struct {
	order    []string
	children map[string]*trieNode
	isFile   bool
}

func newTrieNode() *trieNode {
	return &trieNode{children: map[string]*trieNode{}}
}

// TreeIndex holds the file paths of one revision as a tree of path
// segments.  Listings return entries in the order the paths were added.
type TreeIndex struct {
	root      *trieNode
	fileCount int
}

// NewTreeIndex returns an empty index.
func NewTreeIndex() *TreeIndex {
	return &TreeIndex{root: newTrieNode()}
}

// Add records a file path.
func (t *TreeIndex) Add(path string) {
	current := t.root
	for _, part := range strings.Split(path, "/") {
		child, ok := current.children[part]
		if !ok {
			child = newTrieNode()
			current.children[part] = child
			current.order = append(current.order, part)
		}
		current = child
	}
	if !current.isFile {
		current.isFile = true
		t.fileCount++
	}
}

func (t *TreeIndex) navigate(path string) *trieNode {
	if path == "" {
		return t.root
	}
	current := t.root
	for _, part := range strings.Split(path, "/") {
		child, ok := current.children[part]
		if !ok {
			return nil
		}
		current = child
	}
	return current
}

// Size returns the number of files.
func (t *TreeIndex) Size() int {
	return t.fileCount
}

// PathExists reports whether a file or directory is at path.
func (t *TreeIndex) PathExists(path string) bool {
	if RootPaths[path] {
		return t.fileCount > 0
	}
	return t.navigate(path) != nil
}

// FileExists reports whether a file is at path.
func (t *TreeIndex) FileExists(path string) bool {
	found := t.navigate(path)
	return found != nil && found.isFile
}

// ListChildren returns the names directly below a directory.
func (t *TreeIndex) ListChildren(dir string) []string {
	found := t.navigate(dir)
	if found == nil {
		return []string{}
	}
	return append([]string{}, found.order...)
}

func collectFiles(current *trieNode, prefix string, out *[]string) {
	if current.isFile {
		*out = append(*out, prefix)
	}
	for _, segment := range current.order {
		childPath := segment
		if prefix != "" {
			childPath = prefix + "/" + segment
		}
		collectFiles(current.children[segment], childPath, out)
	}
}

// FilesUnder returns, for each path, the path itself when it is a file, or
// every file below it.
func (t *TreeIndex) FilesUnder(paths ...string) []string {
	result := []string{}
	for _, path := range paths {
		switch {
		case RootPaths[path]:
			collectFiles(t.root, "", &result)
		case t.FileExists(path):
			result = append(result, path)
		default:
			if found := t.navigate(path); found != nil {
				collectFiles(found, path, &result)
			}
		}
	}
	return result
}

// TreeReader answers path questions for the revisions that were indexed.
// A revision without an index has no paths.
type TreeReader struct {
	entries map[string]*TreeIndex
}

// NewTreeReader returns a reader over the indexes by revision.
func NewTreeReader(entries map[string]*TreeIndex) *TreeReader {
	return &TreeReader{entries: entries}
}

var noIndex = NewTreeIndex()

func (r *TreeReader) indexAt(revision string) *TreeIndex {
	if r == nil {
		return noIndex
	}
	if index, ok := r.entries[revision]; ok {
		return index
	}
	return noIndex
}

func (r *TreeReader) PathExists(revision, path string) bool {
	return r.indexAt(revision).PathExists(path)
}

func (r *TreeReader) FileExists(revision, path string) bool {
	return r.indexAt(revision).FileExists(path)
}

func (r *TreeReader) FilesUnder(revision string, paths ...string) []string {
	return r.indexAt(revision).FilesUnder(paths...)
}

func (r *TreeReader) Children(revision, dir string) []string {
	return r.indexAt(revision).ListChildren(dir)
}

// ScopeMatcher returns a function that reports whether a path equals one
// of the scopes or is below one.
func ScopeMatcher(scopes []string) func(path string) bool {
	hasRoot := false
	paths := map[string]bool{}
	for _, scope := range scopes {
		if RootPaths[scope] {
			hasRoot = true
		}
		paths[scope] = true
	}
	return func(path string) bool {
		if hasRoot {
			return true
		}
		for i := 0; i < len(path); i++ {
			if path[i] == '/' && paths[path[:i]] {
				return true
			}
		}
		return paths[path]
	}
}
