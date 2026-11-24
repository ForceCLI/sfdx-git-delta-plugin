package xmldiff

import (
	"strconv"
	"strings"
)

// Object is the content of an element: attributes under keys with the
// prefix "@_", child elements under their tag names, and comments under
// "#comment".  The keys keep the order in which they were first set, which
// is the order the writer uses.
//
// A value is a string (the text of an element without attributes or
// children), true (an attribute without a value), an *Object, or a []any of
// those for a tag that occurs more than once.
type Object struct {
	Keys   []string
	Values map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object {
	return &Object{Values: map[string]any{}}
}

// Get returns the value under key.
func (o *Object) Get(key string) (any, bool) {
	value, ok := o.Values[key]
	return value, ok
}

// Set stores value under key.  A new key is added after the existing ones.
func (o *Object) Set(key string, value any) {
	if _, exists := o.Values[key]; !exists {
		o.Keys = append(o.Keys, key)
	}
	o.Values[key] = value
}

// Len returns the number of keys.
func (o *Object) Len() int {
	return len(o.Keys)
}

const (
	// AttributePrefix starts the key of an attribute.
	AttributePrefix = "@_"
	// XMLHeaderKey is the key of the XML declaration.
	XMLHeaderKey = "?xml"
	commentKey   = "#comment"
	commentStart = "<!--"
	commentEnd   = "-->"
)

func isComment(child any) bool {
	text, ok := child.(string)
	return ok && strings.HasPrefix(text, commentStart)
}

func stripComment(raw string) string {
	return jsSlice(raw, len(commentStart), len(raw)-len(commentEnd))
}

func renderAttributes(n *node) *Object {
	out := NewObject()
	for _, name := range n.attrNames {
		value := n.attrs[name]
		if value == nil {
			out.Set(AttributePrefix+name, true)
		} else {
			out.Set(AttributePrefix+name, *value)
		}
	}
	return out
}

// addChild stores child under key.  A second child with the same key turns
// the value into a list, and later ones are appended to it.
func addChild(out *Object, key string, child any) {
	existing, ok := out.Get(key)
	if !ok {
		out.Set(key, child)
		return
	}
	if list, isList := existing.([]any); isList {
		out.Set(key, append(list, child))
		return
	}
	out.Set(key, []any{existing, child})
}

// nodeToContent converts a parsed element: an element without attributes
// and children is an empty object, an element with only text is that text,
// and any other element is an object.
func nodeToContent(n *node) any {
	attributes := renderAttributes(n)
	if len(n.children) == 0 {
		return attributes
	}
	if attributes.Len() == 0 && len(n.children) == 1 {
		if text, ok := n.children[0].(string); ok && !isComment(text) {
			return text
		}
	}
	out := attributes
	for _, child := range n.children {
		switch typed := child.(type) {
		case string:
			if isComment(typed) {
				addChild(out, commentKey, stripComment(typed))
			} else {
				addChild(out, "#text", typed)
			}
		case *node:
			addChild(out, typed.tagName, nodeToContent(typed))
		}
	}
	return out
}

// ParseXML parses a document into an object with one key for each
// top-level element, "?xml" for the declaration and "#comment" for
// top-level comments.  Empty or malformed input gives an empty object.
func ParseXML(content string) *Object {
	out := NewObject()
	if content == "" {
		return out
	}
	tree, err := txmlParse(content, true)
	if err != nil {
		return NewObject()
	}
	for _, child := range tree {
		switch typed := child.(type) {
		case string:
			if isComment(typed) {
				addChild(out, commentKey, stripComment(typed))
			}
		case *node:
			if typed.tagName == XMLHeaderKey {
				out.Set(XMLHeaderKey, renderAttributes(typed))
				continue
			}
			addChild(out, typed.tagName, nodeToContent(typed))
		}
	}
	return out
}

// jsString is String(value) for the values an Object holds.
func jsString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case *Object:
		return "[object Object]"
	case []any:
		parts := make([]string, len(typed))
		for i, item := range typed {
			parts[i] = jsString(item)
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// fingerprint writes a value in a form that is equal for two values exactly
// when their keys, key order and leaf values are equal.
func fingerprint(b *strings.Builder, value any) {
	switch typed := value.(type) {
	case string:
		b.WriteString(strconv.Quote(typed))
	case bool:
		b.WriteString(strconv.FormatBool(typed))
	case *Object:
		b.WriteByte('{')
		for i, key := range typed.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(key))
			b.WriteByte(':')
			fingerprint(b, typed.Values[key])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				b.WriteByte(',')
			}
			fingerprint(b, item)
		}
		b.WriteByte(']')
	default:
		b.WriteString("null")
	}
}

// Fingerprint returns the fingerprint of a value.
func Fingerprint(value any) string {
	var b strings.Builder
	fingerprint(&b, value)
	return b.String()
}

// DeepEqual reports whether two values have the same structure.  The order
// of the keys of an object does not matter.  The order of a list does.
func DeepEqual(a, b any) bool {
	switch left := a.(type) {
	case string:
		right, ok := b.(string)
		return ok && left == right
	case bool:
		right, ok := b.(bool)
		return ok && left == right
	case *Object:
		right, ok := b.(*Object)
		if !ok {
			return false
		}
		if left == right {
			return true
		}
		if left.Len() != right.Len() {
			return false
		}
		for _, key := range left.Keys {
			other, exists := right.Values[key]
			if !exists || !DeepEqual(left.Values[key], other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !DeepEqual(left[i], right[i]) {
				return false
			}
		}
		return true
	case nil:
		return b == nil
	}
	return false
}
