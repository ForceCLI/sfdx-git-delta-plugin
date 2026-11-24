package registry

import (
	"regexp"
	"strings"
)

// Repository answers which type a file path belongs to.
type Repository struct {
	metadatas  []Metadata
	perExt     map[string]*Metadata
	perDir     map[string]*Metadata
	perXMLName map[string]*Metadata
	// A suffix that more than one type declares names no single type.
	unsafeExtensions map[string]bool
	pathCache        map[string]*Metadata
}

// NewRepository indexes the definitions by suffix, directory and XML name.
// The first definition that declares a suffix keeps it.  The last
// definition that declares a directory or an XML name keeps it.
func NewRepository(metadatas []Metadata) *Repository {
	repository := &Repository{
		metadatas:  metadatas,
		perExt:     map[string]*Metadata{},
		perDir:     map[string]*Metadata{},
		perXMLName: map[string]*Metadata{},
		unsafeExtensions: map[string]bool{
			CustomApplicationSuffix:     true,
			EmailServicesFunctionSuffix: true,
			CustomMetadataSuffix:        true,
		},
		pathCache: map[string]*Metadata{},
	}
	for i := range metadatas {
		metadata := &metadatas[i]
		repository.registerSuffix(metadata)
		// Each suffix in the content of a type is registered with a copy
		// of the type.  The copy keeps the content, which marks the type
		// as the owner of every path below its directory.
		for _, content := range metadata.Content {
			clone := *metadata
			clone.Suffix = content.Suffix
			repository.registerSuffix(&clone)
		}
		if metadata.DirectoryName != "" {
			repository.perDir[metadata.DirectoryName] = metadata
		}
		if metadata.XMLName != "" {
			repository.perXMLName[metadata.XMLName] = metadata
		}
	}
	return repository
}

func (r *Repository) registerSuffix(metadata *Metadata) {
	if metadata.Suffix == "" {
		return
	}
	if _, taken := r.perExt[metadata.Suffix]; taken {
		r.unsafeExtensions[metadata.Suffix] = true
		return
	}
	r.perExt[metadata.Suffix] = metadata
}

// PathOfLine removes the status letter and the white space after it from a
// diff line such as "A\tforce-app/classes/A.cls".  A path without a status
// is returned unchanged unless its second character is white space.
func PathOfLine(line string) string {
	for i, r := range line {
		if i == 0 {
			if r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 {
				return line
			}
			continue
		}
		rest := line[i:]
		trimmed := strings.TrimLeftFunc(rest, isJSWhiteSpace)
		if len(trimmed) == len(rest) {
			return line
		}
		return trimmed
	}
	return line
}

// isJSWhiteSpace reports whether a JavaScript regular expression matches
// the rune with \s.
func isJSWhiteSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', 0x00a0, 0xfeff, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// Has reports whether the path belongs to a known type.
func (r *Repository) Has(path string) bool {
	return r.Get(path) != nil
}

// Get returns the type of the file at path, which can be a diff line.
func (r *Repository) Get(path string) *Metadata {
	return r.resolve(PathOfLine(path))
}

func (r *Repository) resolve(filePath string) *Metadata {
	if cached, ok := r.pathCache[filePath]; ok {
		return cached
	}
	parts := strings.Split(filePath, "/")
	result := r.searchByExtension(parts)
	if result == nil {
		result = r.searchByDirectory(parts)
	}
	r.pathCache[filePath] = result
	return result
}

// Base returns the last segment of a path, as path.posix.parse(p).base
// does: trailing slashes are not part of it.
func Base(filePath string) string {
	trimmed := strings.TrimRight(filePath, "/")
	if trimmed == "" {
		return ""
	}
	return trimmed[strings.LastIndex(trimmed, "/")+1:]
}

// Ext returns the extension of a file name with its dot, as
// path.posix.extname does: a name that starts with its only dot has none.
func Ext(base string) string {
	index := strings.LastIndex(base, ".")
	if index <= 0 || base == ".." {
		return ""
	}
	// A name made of leading dots only, such as "...", has no extension
	// before the last dot either.
	if strings.Trim(base[:index], ".") == "" {
		if index == len(base)-1 {
			return ""
		}
	}
	return base[index:]
}

func (r *Repository) searchByExtension(parts []string) *Metadata {
	name := strings.Replace(parts[len(parts)-1], MetaFileSuffix, "", 1)
	extension := strings.Replace(Ext(Base(name)), ".", "", 1)
	if r.unsafeExtensions[extension] {
		return nil
	}
	return r.perExt[extension]
}

func (r *Repository) searchByDirectory(parts []string) *Metadata {
	var metadata *Metadata
	for _, part := range parts {
		found, ok := r.perDir[part]
		if !ok {
			continue
		}
		metadata = found
		// A type that owns every path below its directory ends the
		// search, so a deeper folder named like the directory of another
		// type does not replace it.
		if ownsNestedPaths(found) {
			break
		}
	}
	return metadata
}

func isContentContainer(metadata *Metadata) bool {
	return ContentContainerAdapters[metadata.Adapter]
}

// ownsNestedPaths reports whether the segments below the directory of the
// type are names chosen by the user and not metadata directories: the type
// is organized in folders, is a content container, or declares content.
func ownsNestedPaths(metadata *Metadata) bool {
	return metadata.InFolder || isContentContainer(metadata) || len(metadata.Content) > 0
}

// GetByXMLName returns the type with the XML name.
func (r *Repository) GetByXMLName(xmlName string) *Metadata {
	return r.perXMLName[xmlName]
}

// Values returns every definition in registry order.
func (r *Repository) Values() []Metadata {
	return r.metadatas
}

var composedTypes = func() map[string]bool {
	types := map[string]bool{
		ObjectType:            true,
		ObjectTranslationType: true,
		PermissionSetType:     true,
		WorkflowType:          true,
		SharingRuleType:       true,
	}
	for _, name := range SubObjectTypes {
		types[name] = true
	}
	return types
}()

// Every file below one PermissionSet or CustomObjectTranslation holder is
// the same component.
var holderScopedComposedTypes = map[string]bool{PermissionSetType: true, ObjectTranslationType: true}

// A BotVersion member is "<bot>.<version>", so its name keeps the bot
// folder.
var folderScopedTypes = map[string]bool{VirtualBotType: true}

var (
	metaSuffixAtEnd      = regexp.MustCompile(regexp.QuoteMeta(MetaFileSuffix) + `$`)
	inFolderSuffixAtEnd  = regexp.MustCompile(InFolderSuffix + `$`)
	extensionSuffixAtEnd = regexp.MustCompile(`\.[^/.]+$`)
)

func withoutMetaSuffix(name string) string {
	return metaSuffixAtEnd.ReplaceAllString(name, "")
}

// FullyQualifiedName returns the name that identifies the component a file
// belongs to.  Two paths with the same name are files of the same
// component, also when they are in different package directories.
func (r *Repository) FullyQualifiedName(path string) string {
	filePath := PathOfLine(path)
	metadata := r.resolve(filePath)
	switch {
	case metadata == nil:
		return Base(filePath)
	case holderScopedComposedTypes[metadata.XMLName]:
		return r.holderScopedName(filePath, metadata)
	case composedTypes[metadata.XMLName]:
		return r.composedTypeName(filePath)
	case !ownsNestedPaths(metadata):
		return plainTypeName(filePath, metadata)
	}
	return r.componentScopedName(filePath, metadata)
}

// plainTypeName is the directory of the type and the file name.  The
// directory comes from the registry and not from the path, because a suffix
// can belong to several types.
func plainTypeName(filePath string, metadata *Metadata) string {
	fileName := withoutMetaSuffix(Base(filePath))
	if metadata.DirectoryName != "" {
		return metadata.DirectoryName + "/" + fileName
	}
	return fileName
}

// composedTypeName joins, without separators, the segments from the first
// one that is a registry directory.  Without such a segment it is the file
// name.
func (r *Repository) composedTypeName(filePath string) string {
	parts := strings.Split(filePath, "/")
	typeIndex := -1
	for i, part := range parts {
		if _, ok := r.perDir[part]; ok {
			typeIndex = i
			break
		}
	}
	if typeIndex == -1 {
		return parts[len(parts)-1]
	}
	return strings.Join(parts[typeIndex:], "")
}

func lastIndexOf(parts []string, value string) int {
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == value {
			return i
		}
	}
	return -1
}

func (r *Repository) holderScopedName(filePath string, metadata *Metadata) string {
	parts := strings.Split(filePath, "/")
	typeIndex := lastIndexOf(parts, metadata.DirectoryName)
	if typeIndex == -1 {
		return Base(filePath)
	}
	return containerName(parts, typeIndex, DefaultContainerDepth)
}

func (r *Repository) componentScopedName(filePath string, metadata *Metadata) string {
	parts := strings.Split(filePath, "/")
	typeIndex := lastIndexOf(parts, metadata.DirectoryName)
	if typeIndex == -1 {
		return Base(filePath)
	}
	// A type can be both organized in folders and a content container
	// (Document).  The folder rule keeps the file below the folder.
	if metadata.InFolder {
		name := withoutMetaSuffix(strings.Join(parts[typeIndex:], "/"))
		name = inFolderSuffixAtEnd.ReplaceAllString(name, "")
		return extensionSuffixAtEnd.ReplaceAllString(name, "")
	}
	if isContentContainer(metadata) {
		nestedCount := len(parts) - typeIndex - 1
		return containerName(parts, typeIndex, containerDepth(metadata.Adapter, nestedCount))
	}
	// Types that share one directory are told apart by extension, so the
	// extension is kept and sub-directories are dropped.
	tail := []string{parts[typeIndex], parts[len(parts)-1]}
	if folderScopedTypes[metadata.XMLName] {
		tail = parts[typeIndex:]
	}
	return withoutMetaSuffix(strings.Join(tail, "/"))
}

// containerName is the type directory and the next depth segments.  When
// those segments reach the end of the path the name is a file, and its
// -meta.xml suffix and extension are removed.  Otherwise it is a directory,
// whose name is kept whole.
func containerName(parts []string, typeIndex, depth int) string {
	nested := parts[typeIndex+1:]
	taken := nested
	if depth < len(nested) {
		taken = nested[:depth]
	}
	name := strings.Join(append([]string{parts[typeIndex]}, taken...), "/")
	if depth >= len(nested) {
		return extensionSuffixAtEnd.ReplaceAllString(withoutMetaSuffix(name), "")
	}
	return name
}

func containerDepth(adapter string, nestedCount int) int {
	if adapter != DigitalExperienceAdapter {
		return DefaultContainerDepth
	}
	if nestedCount > DigitalExperienceContentDepth {
		return DigitalExperienceContentDepth
	}
	return DigitalExperienceBundleDepth
}
