package delta

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/ForceCLI/sfdx-git-delta-plugin/gitadapter"
	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
)

// expandTypeDirCompanionCopies replaces each request for the companion
// files of a bundle type directory by a copy of every file there that
// belongs to no bundle: the files directly in the directory and the files
// of the folders that hold no bundle.  A directory is expanded once for
// each revision.
func expandTypeDirCompanionCopies(copies []CopyOperation, trees *gitadapter.TreeReader) []CopyOperation {
	seen := map[string]bool{}
	var result []CopyOperation
	for _, op := range copies {
		if op.Kind != CopyTypeDirCompanion {
			result = append(result, op)
			continue
		}
		key := op.Revision + "\x00" + op.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, name := range trees.Children(op.Revision, op.Path) {
			entry := op.Path + "/" + name
			if isBundleFolder(trees.Children(op.Revision, entry), name) {
				continue
			}
			for _, file := range trees.FilesUnder(op.Revision, entry) {
				result = append(result, CopyOperation{Kind: CopyFile, Path: file, Revision: op.Revision})
			}
		}
	}
	return result
}

// isBundleFolder reports whether a folder holds a bundle: one of its
// entries is named after the folder and is a -meta.xml file.
func isBundleFolder(entries []string, name string) bool {
	for _, entry := range entries {
		if strings.Split(entry, ".")[0] != name {
			continue
		}
		dot := -1
		if len(entry) > 1 {
			if index := strings.Index(entry[1:], "."); index >= 0 {
				dot = index + 1
			}
		}
		if strings.LastIndex(entry, registry.MetaFileSuffix) >= dot+2 {
			return true
		}
	}
	return false
}

// expandComponentCopies replaces each request for a component by a copy of
// every file of the revision inside the component folder or named after
// the component with a dot and more text.  A component requested more than
// once is expanded at its first request.
func expandComponentCopies(copies []CopyOperation, trees *gitadapter.TreeReader) []CopyOperation {
	type owned struct {
		order []string
		files map[string]*[]string
	}
	byRevision := map[string]*owned{}
	var revisionOrder []string
	filesByFirstOccurrence := map[int]*[]string{}
	for index, op := range copies {
		if op.Kind != CopyComponent {
			continue
		}
		components := byRevision[op.Revision]
		if components == nil {
			components = &owned{files: map[string]*[]string{}}
			byRevision[op.Revision] = components
			revisionOrder = append(revisionOrder, op.Revision)
		}
		if _, exists := components.files[op.Path]; exists {
			continue
		}
		files := &[]string{}
		components.files[op.Path] = files
		components.order = append(components.order, op.Path)
		filesByFirstOccurrence[index] = files
	}
	for _, revision := range revisionOrder {
		components := byRevision[revision]
		seenParents := map[string]bool{}
		for _, component := range components.order {
			parent := ""
			if index := strings.LastIndex(component, "/"); index >= 0 {
				parent = component[:index]
			}
			if seenParents[parent] {
				continue
			}
			seenParents[parent] = true
			nameStart := len(parent) + 1
			for _, file := range trees.FilesUnder(revision, parent) {
				// The file belongs to each requested path that is a
				// prefix of it ending before a dot, or before the first
				// slash after the parent directory.
				for cut := nextCut(file, nameStart); cut >= 0; cut = nextCut(file, cut+1) {
					if files, ok := components.files[file[:cut]]; ok {
						*files = append(*files, file)
					}
					if file[cut] != '.' {
						break
					}
				}
			}
		}
	}
	var result []CopyOperation
	for index, op := range copies {
		if op.Kind != CopyComponent {
			result = append(result, op)
			continue
		}
		if files, ok := filesByFirstOccurrence[index]; ok {
			for _, file := range *files {
				result = append(result, CopyOperation{Kind: CopyFile, Path: file, Revision: op.Revision})
			}
		}
	}
	return result
}

func nextCut(file string, from int) int {
	if from > len(file) {
		return -1
	}
	offset := strings.IndexAny(file[from:], "./")
	if offset < 0 {
		return -1
	}
	return from + offset
}

// executor writes the copy operations below the output directory.
type executor struct {
	ctx                 *RunContext
	processedPaths      map[string]bool
	writtenDestinations map[string]bool
	failedDestinations  map[string]bool
	warnings            []string
}

func newExecutor(ctx *RunContext) *executor {
	return &executor{ctx: ctx, processedPaths: map[string]bool{}, writtenDestinations: map[string]bool{}, failedDestinations: map[string]bool{}}
}

func (e *executor) ignored(path string) bool {
	return e.ctx.shared.ignore.Global.Ignores(path)
}

// destination returns the path below the output directory for a
// repository path.  The second result is false for a path that would be
// written outside the output directory.
func (e *executor) destination(path string) (string, bool) {
	dst := filepath.Join(e.ctx.Config.Output, filepath.FromSlash(path))
	output, err := filepath.Abs(e.ctx.Config.Output)
	if err != nil {
		return "", false
	}
	absolute, err := filepath.Abs(dst)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(output, absolute)
	if err != nil || relative == "." || relative == "" || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return "", false
	}
	return dst, true
}

// write puts content at dst through a temporary file next to it, so a
// reader of the output directory never sees part of a file.  A failure is
// reported as a warning and the destination is remembered as failed.
func (e *executor) write(dst string, content []byte) {
	err := os.MkdirAll(filepath.Dir(dst), 0o755)
	if err == nil {
		temporary := dst + ".tmp"
		if err = os.WriteFile(temporary, content, 0o644); err == nil {
			err = os.Rename(temporary, dst)
		}
		if err != nil {
			os.Remove(temporary)
		}
	}
	if err != nil {
		e.failedDestinations[dst] = true
		e.warnings = append(e.warnings, message("warning.OutputWriteFailed", sanitizeForMessage(dst), sanitizeForMessage(err.Error())))
		return
	}
	e.writtenDestinations[dst] = true
}

func (e *executor) copyFile(path, revision string) {
	dst, ok := e.destination(path)
	if !ok {
		return
	}
	content, err := e.ctx.Git.BufferContent(path, revision)
	if err != nil {
		// A file that is not in the revision is not copied.
		return
	}
	e.write(dst, content)
}

func (e *executor) writeStreamed(path string, writer func(out *bytes.Buffer) error) {
	dst, ok := e.destination(path)
	if !ok {
		return
	}
	var buffer bytes.Buffer
	if err := writer(&buffer); err != nil {
		return
	}
	e.write(dst, buffer.Bytes())
}

// execute writes the operations in order.  A path is written by the first
// operation that names it.  Paths the global ignore patterns match are not
// written.  The operations that rewrite an existing file run last.
func (e *executor) execute(copies []CopyOperation) {
	trees := e.ctx.Trees
	fileCopies := expandComponentCopies(expandTypeDirCompanionCopies(copies, trees), trees)
	var augments []CopyOperation
	for _, op := range fileCopies {
		if op.Kind == CopyAugmented {
			augments = append(augments, op)
			continue
		}
		if e.processedPaths[op.Path] {
			continue
		}
		e.processedPaths[op.Path] = true
		if e.ignored(op.Path) {
			continue
		}
		switch op.Kind {
		case CopyFile:
			e.copyFile(op.Path, op.Revision)
		case CopyDir:
			e.copyDir(op)
		case CopyStreamed:
			writer := op.Writer
			e.writeStreamed(op.Path, func(out *bytes.Buffer) error { return writer(out) })
		}
	}
	for _, op := range augments {
		if e.ignored(op.Path) {
			continue
		}
		dst, ok := e.destination(op.Path)
		if !ok || e.failedDestinations[dst] {
			continue
		}
		written, present := "", false
		if e.writtenDestinations[dst] {
			content, err := os.ReadFile(dst)
			if err != nil {
				continue
			}
			written, present = string(content), true
		}
		augment := op.Augment
		e.writeStreamed(op.Path, func(out *bytes.Buffer) error { return augment(out, written, present) })
	}
}

// dirCopyArchiveThreshold is the number of files above which upstream
// copies a directory through an archive stream, which skips the files an
// earlier operation wrote.
const dirCopyArchiveThreshold = 25

func (e *executor) copyDir(op CopyOperation) {
	files := trees(e).FilesUnder(op.Revision, op.Path)
	skipProcessed := len(files) > dirCopyArchiveThreshold
	for _, file := range files {
		if skipProcessed && e.processedPaths[file] {
			continue
		}
		if e.ignored(file) {
			continue
		}
		if skipProcessed {
			e.processedPaths[file] = true
		}
		dst, ok := e.destination(file)
		if !ok {
			continue
		}
		content, err := e.ctx.Git.BufferContent(file, op.Revision)
		if err != nil {
			return
		}
		e.write(dst, content)
		e.processedPaths[file] = true
	}
}

func trees(e *executor) *gitadapter.TreeReader {
	return e.ctx.Trees
}
