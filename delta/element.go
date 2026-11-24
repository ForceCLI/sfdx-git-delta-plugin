package delta

import (
	"strings"

	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
)

// Element is a file path together with the metadata type it belongs to.
type Element struct {
	FullPath string
	// BasePath is the path without the -meta.xml suffix for a type whose
	// components have a meta file next to their content.
	BasePath     string
	Parts        []string
	Extension    string
	ParentFolder string
	IsMetaFile   bool

	definition  *registry.Metadata
	repository  *registry.Repository
	anchorIndex int
}

func newElement(fullPath string, definition *registry.Metadata, repository *registry.Repository, anchorIndex int) *Element {
	element := &Element{
		FullPath:    fullPath,
		IsMetaFile:  strings.HasSuffix(fullPath, registry.MetaFileSuffix),
		Parts:       strings.Split(fullPath, "/"),
		definition:  definition,
		repository:  repository,
		anchorIndex: anchorIndex,
	}
	element.BasePath = fullPath
	if element.IsMetaFile && definition.MetaFile {
		element.BasePath = strings.Replace(fullPath, registry.MetaFileSuffix, "", 1)
	}
	parsed := parsePath(element.BasePath)
	segments := strings.Split(strings.Replace(parsed.base, registry.MetaFileSuffix, "", 1), ".")
	element.Extension = segments[len(segments)-1]
	element.ParentFolder = lastSegment(parsed.dir)
	return element
}

// elementFromPath anchors the element at the type directory in the path,
// or at the folder below it when the file is deeper.  It returns nil when
// the path does not contain the type directory.
func elementFromPath(path string, definition *registry.Metadata, repository *registry.Repository) *Element {
	parts := strings.Split(path, "/")
	dirIndex := lastIndexOf(parts, definition.DirectoryName)
	if dirIndex < 0 {
		return nil
	}
	anchorIndex := dirIndex
	if dirIndex+1 < len(parts)-1 {
		anchorIndex = dirIndex + 1
	}
	return newElement(path, definition, repository, anchorIndex)
}

// elementFromScan anchors the element at the last segment that is the
// component name or starts with the name and a dot.
func elementFromScan(path string, definition *registry.Metadata, repository *registry.Repository, componentName string) *Element {
	parts := strings.Split(path, "/")
	anchorIndex := lastIndexOf(parts, componentName)
	if anchorIndex < 0 {
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.HasPrefix(parts[i], componentName+".") {
				anchorIndex = i
				break
			}
		}
	}
	if anchorIndex < 0 {
		anchorIndex = len(parts) - 1
	}
	return newElement(path, definition, repository, anchorIndex)
}

// Type returns the metadata type of the element.
func (e *Element) Type() *registry.Metadata {
	return e.definition
}

// ComponentName returns the file name without -meta.xml and extension.
func (e *Element) ComponentName() string {
	return parsePath(withoutMetaSuffixAtEnd(e.Parts[len(e.Parts)-1])).name
}

func (e *Element) dirIndex() int {
	return lastIndexOf(e.Parts, e.definition.DirectoryName)
}

func sliceOf(parts []string, start, end int) []string {
	if start < 0 {
		start = 0
	}
	if end > len(parts) {
		end = len(parts)
	}
	if start >= end {
		return []string{}
	}
	return parts[start:end]
}

// ParentName returns the segment before the type directory.
func (e *Element) ParentName() string {
	if dirIndex := e.dirIndex(); dirIndex >= 1 {
		return e.Parts[dirIndex-1]
	}
	if e.anchorIndex >= 2 {
		return e.Parts[e.anchorIndex-2]
	}
	return ""
}

// ComponentPath returns the path of the component: up to the anchor
// segment for a file below a component folder, or the type directory and
// the component name.
func (e *Element) ComponentPath() string {
	if len(e.PathAfterType()) > 1 {
		return strings.Join(sliceOf(e.Parts, 0, e.anchorIndex+1), "/")
	}
	return e.TypeDirectoryPath() + "/" + e.ComponentName()
}

// TypeDirectoryPath returns the path up to and including the type
// directory.
func (e *Element) TypeDirectoryPath() string {
	if dirIndex := e.dirIndex(); dirIndex >= 0 {
		return strings.Join(e.Parts[:dirIndex+1], "/")
	}
	return strings.Join(sliceOf(e.Parts, 0, e.anchorIndex), "/")
}

// PathAfterType returns the segments after the type directory.
func (e *Element) PathAfterType() []string {
	if dirIndex := e.dirIndex(); dirIndex >= 0 {
		return e.Parts[dirIndex+1:]
	}
	return sliceOf(e.Parts, e.anchorIndex, len(e.Parts))
}

// ParentType returns the type that contains the type of the element.
func (e *Element) ParentType() *registry.Metadata {
	if e.definition.ParentXMLName == "" {
		return nil
	}
	return e.repository.GetByXMLName(e.definition.ParentXMLName)
}

// boundaryResolver finds, for a path, the component it belongs to.
type boundaryResolver struct {
	ctx            *RunContext
	dirCache       map[string][]string
	componentNames map[string]map[string]bool
}

func newBoundaryResolver(ctx *RunContext) *boundaryResolver {
	return &boundaryResolver{ctx: ctx, dirCache: map[string][]string{}, componentNames: map[string]map[string]bool{}}
}

func extractName(fileName, suffix string) string {
	return strings.Replace(strings.Replace(fileName, registry.MetaFileSuffix, "", 1), "."+suffix, "", 1)
}

func (r *boundaryResolver) createElement(path string, definition *registry.Metadata, revision string) *Element {
	element := elementFromPath(path, definition, r.ctx.Metadata)
	if element != nil {
		after := element.PathAfterType()
		if len(after) <= 1 || definition.Suffix == "" {
			return element
		}
		if len(after) == 2 {
			fileName := after[1]
			if strings.Contains(fileName, "."+definition.Suffix) {
				componentName := extractName(fileName, definition.Suffix)
				if componentName == after[0] {
					return element
				}
				return elementFromScan(path, definition, r.ctx.Metadata, componentName)
			}
			return element
		}
	}
	parts := strings.Split(path, "/")
	dirIndex := lastIndexOf(parts, definition.DirectoryName)
	componentName := ""
	found := false
	if dirIndex >= 0 && definition.Suffix != "" {
		componentName, found = r.findNameUnderTypeDirectory(parts, dirIndex, definition.Suffix, revision)
	} else {
		componentName, found = r.findNameInAncestors(path, parts, revision)
	}
	if !found {
		componentName = parsePath(path).name
	}
	return elementFromScan(path, definition, r.ctx.Metadata, componentName)
}

// findNameUnderTypeDirectory returns the deepest folder of the path that is
// the name of a component with a meta file below the type directory.
func (r *boundaryResolver) findNameUnderTypeDirectory(parts []string, dirIndex int, suffix, revision string) (string, bool) {
	typeDir := strings.Join(parts[:dirIndex+1], "/")
	cacheKey := revision + ":" + suffix + ":" + typeDir
	names, cached := r.componentNames[cacheKey]
	if !cached {
		names = map[string]bool{}
		metaSuffix := "." + suffix + registry.MetaFileSuffix
		for _, file := range r.ctx.Trees.FilesUnder(revision, typeDir) {
			if strings.HasSuffix(file, metaSuffix) {
				names[extractName(lastSegment(file), suffix)] = true
			}
		}
		r.componentNames[cacheKey] = names
	}
	after := parts[dirIndex+1:]
	for i := len(after) - 2; i >= 0; i-- {
		if names[after[i]] {
			return after[i], true
		}
	}
	return "", false
}

// findNameInAncestors looks, in each directory above the file, for a file
// of a known type whose component name is a segment of the path.
func (r *boundaryResolver) findNameInAncestors(path string, parts []string, revision string) (string, bool) {
	for currentDir := dirname(path); currentDir != "" && currentDir != "."; currentDir = dirname(currentDir) {
		cacheKey := revision + ":" + currentDir
		siblings, cached := r.dirCache[cacheKey]
		if !cached {
			siblings = r.ctx.Trees.Children(revision, currentDir)
			r.dirCache[cacheKey] = siblings
		}
		for _, sibling := range siblings {
			siblingType := r.ctx.Metadata.Get(sibling)
			if siblingType == nil || siblingType.Suffix == "" || !strings.Contains(sibling, "."+siblingType.Suffix) {
				continue
			}
			name := extractName(sibling, siblingType.Suffix)
			for _, part := range parts {
				if part == name || strings.HasPrefix(part, name+".") {
					return name, true
				}
			}
		}
		if currentDir == "/" {
			break
		}
	}
	return "", false
}
