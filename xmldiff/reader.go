package xmldiff

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// RootCapture is what the reader keeps of a document besides the children
// of its root: the XML declaration, the root tag name and its attributes.
type RootCapture struct {
	// XMLHeader is an object with the key "?xml", or nil when the document
	// has no declaration.
	XMLHeader      *Object
	RootKey        string
	RootAttributes *Object
}

// ElementHandler receives each child element of the root: its tag name and
// its content.
type ElementHandler func(subType string, element any)

var rootOpenTag = regexp.MustCompile(`^<([A-Za-z_:][A-Za-z0-9_.:\-]*)([^>]*)>`)

type prologue struct {
	xmlHeader      *Object
	rootName       string
	rootAttributes *Object
	bodyStart      int
	isSelfClosing  bool
}

func skipJSWhiteSpace(s string, i int) int {
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !isJSWhiteSpace(r) {
			break
		}
		i += size
	}
	return i
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// declarationLength returns the length of the XML declaration at the start
// of the document, with the white space before it, or 0 when there is none.
func declarationLength(s string) int {
	i := skipJSWhiteSpace(s, 0)
	if !strings.HasPrefix(s[i:], "<?xml") {
		return 0
	}
	i += len("<?xml")
	if i < len(s) && isWordByte(s[i]) {
		return 0
	}
	end := strings.IndexByte(s[i:], '?')
	if end < 0 || !strings.HasPrefix(s[i+end:], "?>") {
		return 0
	}
	return i + end + 2
}

func parseDeclaration(declaration string) *Object {
	header := NewObject()
	attributes := NewObject()
	header.Set(XMLHeaderKey, attributes)
	tree, err := txmlParse(declaration, false)
	if err != nil {
		return header
	}
	for _, child := range tree {
		if typed, ok := child.(*node); ok && typed.tagName == XMLHeaderKey {
			header.Set(XMLHeaderKey, renderAttributes(typed))
			break
		}
	}
	return header
}

// skipPrologueMisc returns the offset of the first character after start
// that is not white space, a comment or a "<!...>" declaration, or -1 when
// a declaration is not closed.
func skipPrologueMisc(s string, start int) int {
	i := start
	for i < len(s) {
		if next := skipJSWhiteSpace(s, i); next > i {
			i = next
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := indexFrom(s, "-->", i+4)
			if end >= 0 {
				i = end + 3
				continue
			}
			// An unterminated comment is not skipped; the root tag
			// match then fails.
			break
		}
		if strings.HasPrefix(s[i:], "<!") {
			end := indexFrom(s, ">", i+2)
			if end < 0 {
				return -1
			}
			i = end + 1
			continue
		}
		break
	}
	return i
}

func parsePrologue(s string) (*prologue, error) {
	declaration := s[:declarationLength(s)]
	result := &prologue{}
	if declaration != "" {
		result.xmlHeader = parseDeclaration(declaration)
	}
	rootStart := skipPrologueMisc(s, len(declaration))
	if rootStart < 0 {
		return nil, nil
	}
	match := rootOpenTag.FindStringSubmatch(s[rootStart:])
	if match == nil {
		return nil, nil
	}
	result.rootName = match[1]
	result.bodyStart = rootStart + len(match[0])
	result.isSelfClosing = strings.HasSuffix(jsTrimEnd(match[2]), "/")
	// The attributes of the root are parsed by the same parser as every
	// other tag, from the tag written as an empty element.
	result.rootAttributes = NewObject()
	tree, err := txmlParse("<"+match[1]+match[2]+"/>", false)
	if err != nil {
		return nil, err
	}
	for _, child := range tree {
		typed, ok := child.(*node)
		if !ok {
			continue
		}
		for _, name := range typed.attrNames {
			value := "true"
			if typed.attrs[name] != nil {
				value = *typed.attrs[name]
			}
			result.rootAttributes.Set(AttributePrefix+name, value)
		}
		break
	}
	return result, nil
}

// streamRootChildren passes each child element of the root to onElement
// and returns the offset where it stopped: at the close tag of the root, or
// at the end of the input.
func streamRootChildren(s string, bodyStart int, onElement ElementHandler) (int, error) {
	pos := bodyStart
	for pos < len(s) {
		lt := indexFrom(s, "<", pos)
		if lt < 0 {
			return len(s), nil
		}
		if strings.HasPrefix(s[lt:], "</") {
			return lt, nil
		}
		if strings.HasPrefix(s[lt:], "<!--") {
			end := indexFrom(s, "-->", lt+4)
			if end < 0 {
				pos = len(s)
			} else {
				pos = end + 3
			}
			continue
		}
		child, after, err := txmlParseNode(s, lt)
		if err != nil {
			return 0, err
		}
		// The parser made no progress: the scan ends.
		if after-lt <= 0 {
			pos = len(s)
			continue
		}
		content := nodeToContent(child)
		onElement(child.tagName, content)
		pos = after
	}
	return pos, nil
}

// verifyTail checks that the document closes its root and that only white
// space and comments follow.
func verifyTail(s string, pos int, rootName string, isSelfClosing bool) error {
	i := pos
	if !isSelfClosing {
		expected := "</" + rootName + ">"
		if !strings.HasPrefix(jsSlice(s, i, len(s)), expected) {
			return fmt.Errorf("unclosed or mismatched root <%s> at offset %d", rootName, i)
		}
		i += len(expected)
	}
	for i < len(s) {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := indexFrom(s, "-->", i+4)
			if end < 0 {
				return fmt.Errorf("unterminated comment after root close")
			}
			i = end + 3
			continue
		}
		return fmt.Errorf("unexpected content after root close: %s", s[i:min(i+30, len(s))])
	}
	return nil
}

func driveParse(source string, onElement ElementHandler, strict bool) (*RootCapture, error) {
	parsed, err := parsePrologue(source)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, nil
	}
	endPos := parsed.bodyStart
	if !parsed.isSelfClosing {
		if endPos, err = streamRootChildren(source, parsed.bodyStart, onElement); err != nil {
			return nil, err
		}
	}
	if strict {
		if err := verifyTail(source, endPos, parsed.rootName, parsed.isSelfClosing); err != nil {
			return nil, err
		}
	}
	return &RootCapture{XMLHeader: parsed.xmlHeader, RootKey: parsed.rootName, RootAttributes: parsed.rootAttributes}, nil
}

// ParseFromSide reads the earlier revision of a file.  An empty or
// malformed document gives nil: the file is then treated as having no
// earlier content.  Elements passed to onElement before a failure stay
// passed.
func ParseFromSide(source string, onElement ElementHandler) *RootCapture {
	if source == "" {
		return nil
	}
	capture, err := driveParse(source, onElement, false)
	if err != nil {
		return nil
	}
	return capture
}

// ParseToSide reads the later revision of a file and returns an error for a
// document that has no root, does not close its root, or has content after
// it.
func ParseToSide(source string, onElement ElementHandler) (*RootCapture, error) {
	capture, err := driveParse(source, onElement, true)
	if err != nil {
		return nil, err
	}
	if capture == nil {
		return nil, fmt.Errorf("to-side document has no root element")
	}
	return capture, nil
}
