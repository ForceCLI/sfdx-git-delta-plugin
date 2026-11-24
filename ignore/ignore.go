// Package ignore matches paths against patterns in gitignore syntax.
//
// Upstream uses the npm package "ignore".  This package follows the same
// rules: patterns match without regard to letter case, the last pattern
// that matches a path decides, and a path below an ignored directory is
// ignored whatever later patterns say.
package ignore

import (
	"regexp"
	"strings"
)

type rule struct {
	negative bool
	dirOnly  bool
	regex    *regexp.Regexp
}

// Matcher holds the patterns of one ignore file.
type Matcher struct {
	rules []rule
}

// neverMatches is a class no character is in.
const neverMatches = `[^\x{0}-\x{10FFFF}]`

// Parse reads the lines of an ignore file.  Empty lines and lines that
// start with # are skipped.
func Parse(content string) *Matcher {
	matcher := &Matcher{}
	content = strings.TrimPrefix(content, "\xef\xbb\xbf")
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if parsed, ok := parseRule(line); ok {
			matcher.rules = append(matcher.rules, parsed)
		}
	}
	return matcher
}

// trimTrailingSpaces removes the spaces at the end of a pattern that are
// not escaped with a backslash.
func trimTrailingSpaces(pattern string) string {
	end := len(pattern)
	for end > 0 && pattern[end-1] == ' ' {
		backslashes := 0
		for i := end - 2; i >= 0 && pattern[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			break
		}
		end--
	}
	return pattern[:end]
}

func parseRule(line string) (rule, bool) {
	pattern := trimTrailingSpaces(line)
	if pattern == "" || strings.HasPrefix(pattern, "#") {
		return rule{}, false
	}
	parsed := rule{}
	if strings.HasPrefix(pattern, "!") {
		parsed.negative = true
		pattern = pattern[1:]
	} else if strings.HasPrefix(pattern, `\!`) || strings.HasPrefix(pattern, `\#`) {
		pattern = pattern[1:]
	}
	if strings.HasSuffix(pattern, "/") && !strings.HasSuffix(pattern, `\/`) {
		parsed.dirOnly = true
		pattern = strings.TrimRight(pattern, "/")
	}
	if pattern == "" {
		return rule{}, false
	}
	// A pattern with a slash that is not at its end is relative to the
	// root.  Another pattern matches a name at any depth.
	anchored := strings.Contains(pattern, "/")
	pattern = strings.TrimLeft(pattern, "/")
	if pattern == "" {
		return rule{}, false
	}
	var source strings.Builder
	source.WriteString("(?is)^")
	if !anchored {
		source.WriteString("(?:.*/)?")
	}
	segments := strings.Split(pattern, "/")
	for i, segment := range segments {
		last := i == len(segments)-1
		switch {
		case segment == "**" && last && i > 0:
			source.WriteString(".+")
		case segment == "**" && !last:
			source.WriteString("(?:[^/]+/)*")
		default:
			source.WriteString(segmentRegex(segment))
			if !last {
				source.WriteString("/")
			}
		}
	}
	source.WriteString("$")
	compiled, err := regexp.Compile(source.String())
	if err != nil {
		return rule{}, false
	}
	parsed.regex = compiled
	return parsed, true
}

// segmentRegex translates one path segment of a pattern: * is any text
// without a slash, ? is one character that is not a slash, and [...] is a
// character class.
func segmentRegex(segment string) string {
	var out strings.Builder
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch c {
		case '\\':
			if i+1 < len(segment) {
				i++
				out.WriteString(regexp.QuoteMeta(segment[i : i+1]))
			}
		case '*':
			for i+1 < len(segment) && segment[i+1] == '*' {
				i++
			}
			out.WriteString("[^/]*")
		case '?':
			out.WriteString("[^/]")
		case '[':
			end := classEnd(segment, i)
			if end < 0 {
				return neverMatches
			}
			body := segment[i+1 : end]
			negated := false
			if strings.HasPrefix(body, "!") || strings.HasPrefix(body, "^") {
				negated = true
				body = body[1:]
			}
			body = strings.ReplaceAll(body, `\`, `\\`)
			if negated {
				out.WriteString("[^/" + body + "]")
			} else {
				out.WriteString("[" + body + "]")
			}
			i = end
		default:
			out.WriteString(regexp.QuoteMeta(segment[i : i+1]))
		}
	}
	return out.String()
}

// classEnd returns the index of the ] that closes the class opened at
// start, or -1.  A ] right after the opening, or after a negation mark, is
// a member of the class.
func classEnd(segment string, start int) int {
	i := start + 1
	if i < len(segment) && (segment[i] == '!' || segment[i] == '^') {
		i++
	}
	if i < len(segment) && segment[i] == ']' {
		i++
	}
	for ; i < len(segment); i++ {
		if segment[i] == '[' && i+1 < len(segment) && segment[i+1] == ':' {
			if close := strings.Index(segment[i:], ":]"); close >= 0 {
				i += close + 1
				continue
			}
		}
		if segment[i] == ']' {
			return i
		}
	}
	return -1
}

func (m *Matcher) test(path string, isDir bool) bool {
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.regex.MatchString(path) {
			ignored = !r.negative
		}
	}
	return ignored
}

// Ignores reports whether a file path, relative to the root and with
// forward slashes, is ignored: one of its parent directories is, or the
// last pattern that matches the path is not a negation.
func (m *Matcher) Ignores(path string) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '/' && i > 0 && m.test(path[:i], true) {
			return true
		}
	}
	return m.test(strings.TrimRight(path, "/"), strings.HasSuffix(path, "/"))
}

// Helper applies an ignore file to diff lines: one set of patterns for
// additions and modifications, and one for deletions.
type Helper struct {
	Global      *Matcher
	Destructive *Matcher
}

// Keep reports whether the path of a diff line is not ignored.  A line
// that starts with D is checked against the destructive patterns, and one
// that starts with A or M against the global patterns.
func (h *Helper) Keep(line string) bool {
	if h == nil || line == "" {
		return true
	}
	var matcher *Matcher
	switch line[0] {
	case 'D':
		matcher = h.Destructive
	case 'A', 'M':
		matcher = h.Global
	}
	if matcher == nil {
		return true
	}
	path := line
	if tab := strings.IndexAny(line, "\t "); tab == 1 {
		path = strings.TrimLeft(line[1:], "\t ")
	}
	return !matcher.Ignores(path)
}
