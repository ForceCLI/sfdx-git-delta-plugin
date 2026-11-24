package delta

import (
	"path"
	"strings"
	"unicode"

	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
)

func isControlOrFormat(r rune) bool {
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029
}

// parsedPath is the result of path.posix.parse.
type parsedPath struct {
	dir  string
	base string
	ext  string
	name string
}

// parsePath splits a path as path.posix.parse does: dir is the text before
// the last slash and is empty for a path without one.
func parsePath(p string) parsedPath {
	trimmed := p
	for len(trimmed) > 1 && strings.HasSuffix(trimmed, "/") {
		trimmed = trimmed[:len(trimmed)-1]
	}
	result := parsedPath{}
	if index := strings.LastIndex(trimmed, "/"); index >= 0 {
		result.dir = trimmed[:index]
		if index == 0 {
			result.dir = "/"
		}
		result.base = trimmed[index+1:]
	} else {
		result.base = trimmed
	}
	result.ext = registry.Ext(result.base)
	result.name = strings.TrimSuffix(result.base, result.ext)
	return result
}

// joinPath is path.posix.join for the paths this program builds.
func joinPath(parts ...string) string {
	return path.Join(parts...)
}

// dirname is path.posix.dirname.
func dirname(p string) string {
	return path.Dir(p)
}

func lastIndexOf(parts []string, value string) int {
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == value {
			return i
		}
	}
	return -1
}

func lastSegment(p string) string {
	return p[strings.LastIndex(p, "/")+1:]
}

func withoutMetaSuffixAtEnd(name string) string {
	return strings.TrimSuffix(name, registry.MetaFileSuffix)
}

// withoutExtension removes a last extension that has no slash or dot in it,
// as the expression /\.[^/.]+$/ does.
func withoutExtension(name string) string {
	index := strings.LastIndex(name, ".")
	if index < 0 || index == len(name)-1 || strings.Contains(name[index:], "/") {
		return name
	}
	return name[:index]
}
