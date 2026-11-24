package xmldiff

import (
	"bufio"
	"io"
	"strings"
)

// Entry is a child of the root passed to the writer: a tag name and its
// content, or a list of contents for a tag that occurs more than once.
type Entry struct {
	Key   string
	Value any
}

// WriteOptions change how WriteDocument writes.
type WriteOptions struct {
	// NoTrailingNewline leaves out the line break after the close tag of
	// the root.
	NoTrailingNewline bool
	// Escape replaces &, < and > in element text.  Text read by this
	// package is already escaped and is written as it was read.
	Escape bool
}

const indentUnit = "    "

// EscapeText replaces the characters that cannot appear in element text.
func EscapeText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return strings.ReplaceAll(value, ">", "&gt;")
}

func renderAttrs(attributes *Object) string {
	if attributes == nil {
		return ""
	}
	var b strings.Builder
	for _, key := range attributes.Keys {
		b.WriteString(" " + jsSlice(key, len(AttributePrefix), len(key)) + `="` + jsString(attributes.Values[key]) + `"`)
	}
	return b.String()
}

func splitAttributesAndChildren(content *Object) (*Object, []Entry) {
	attributes := NewObject()
	var children []Entry
	for _, key := range content.Keys {
		value := content.Values[key]
		if strings.HasPrefix(key, AttributePrefix) {
			attributes.Set(key, jsString(value))
			continue
		}
		children = append(children, Entry{Key: key, Value: value})
	}
	return attributes, children
}

type documentWriter struct {
	out    *bufio.Writer
	escape bool
}

func (w *documentWriter) element(name string, attributes *Object, children []Entry, depth int, trailingNewline bool) {
	indent := strings.Repeat(indentUnit, depth)
	ending := ""
	if trailingNewline {
		ending = "\n"
	}
	if len(children) == 0 {
		w.out.WriteString(indent + "<" + name + renderAttrs(attributes) + "></" + name + ">" + ending)
		return
	}
	w.out.WriteString(indent + "<" + name + renderAttrs(attributes) + ">\n")
	for _, child := range children {
		if child.Key == commentKey {
			if list, ok := child.Value.([]any); ok {
				for _, item := range list {
					w.comment(jsString(item), depth+1)
				}
			} else {
				w.comment(jsString(child.Value), depth+1)
			}
			continue
		}
		if list, ok := child.Value.([]any); ok {
			for _, item := range list {
				w.child(child.Key, item, depth+1)
			}
			continue
		}
		w.child(child.Key, child.Value, depth+1)
	}
	w.out.WriteString(indent + "</" + name + ">" + ending)
}

func (w *documentWriter) comment(value string, depth int) {
	w.out.WriteString(strings.Repeat(indentUnit, depth) + "<!--" + value + "-->\n")
}

func (w *documentWriter) child(key string, value any, depth int) {
	if object, ok := value.(*Object); ok {
		attributes, children := splitAttributesAndChildren(object)
		w.element(key, attributes, children, depth, true)
		return
	}
	text := jsString(value)
	if w.escape {
		text = EscapeText(text)
	}
	w.out.WriteString(strings.Repeat(indentUnit, depth) + "<" + key + ">" + text + "</" + key + ">\n")
}

// WriteDocument writes a document: the declaration of the capture, the root
// with its attributes, and the children indented by four spaces for each
// level.  An element without children is written as "<a></a>".
func WriteDocument(out io.Writer, capture *RootCapture, rootChildren []Entry, options WriteOptions) error {
	buffered := bufio.NewWriter(out)
	writer := &documentWriter{out: buffered, escape: options.Escape}
	if capture.XMLHeader != nil {
		if declaration, ok := capture.XMLHeader.Values[XMLHeaderKey].(*Object); ok && declaration != nil {
			attributes, _ := splitAttributesAndChildren(declaration)
			buffered.WriteString("<?xml" + renderAttrs(attributes) + "?>\n")
		}
	}
	writer.element(capture.RootKey, capture.RootAttributes, rootChildren, 0, !options.NoTrailingNewline)
	return buffered.Flush()
}
