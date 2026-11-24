// Package xmldiff compares two revisions of a metadata XML file element by
// element and writes the elements that were added or changed.
//
// Upstream parses XML with the txml JavaScript library and its output
// depends on what that parser accepts and how it splits text, so this file
// follows txml 6.0.3 statement by statement, including its handling of
// malformed input.
package xmldiff

import (
	"fmt"
	"strings"
)

// node is an element as txml returns it.  A child is a *node or a string
// (text, a comment with its markers, CDATA content or a declaration).
type node struct {
	tagName   string
	attrNames []string
	attrs     map[string]*string
	children  []any
}

func (n *node) setAttr(name string, value *string) {
	if _, exists := n.attrs[name]; !exists {
		n.attrNames = append(n.attrNames, name)
	}
	n.attrs[name] = value
}

// isJSWhiteSpace reports whether String.prototype.trim removes the rune.
func isJSWhiteSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', 0x00a0, 0xfeff, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func jsTrim(s string) string {
	return strings.TrimFunc(s, isJSWhiteSpace)
}

func jsTrimEnd(s string) string {
	return strings.TrimRightFunc(s, isJSWhiteSpace)
}

// jsSlice is String.prototype.slice: a negative index counts from the end.
func jsSlice(s string, start, end int) string {
	length := len(s)
	if start < 0 {
		start = max(length+start, 0)
	} else {
		start = min(start, length)
	}
	if end < 0 {
		end = max(length+end, 0)
	} else {
		end = min(end, length)
	}
	if start >= end {
		return ""
	}
	return s[start:end]
}

// jsSubstring is String.prototype.substring: a negative index is 0 and the
// two indexes are swapped when the first is larger.
func jsSubstring(s string, start, end int) string {
	length := len(s)
	start = min(max(start, 0), length)
	end = min(max(end, 0), length)
	if start > end {
		start, end = end, start
	}
	return s[start:end]
}

// indexFrom is String.prototype.indexOf with a start position.
func indexFrom(s, substr string, from int) int {
	if from < 0 {
		from = 0
	}
	if from > len(s) {
		return -1
	}
	index := strings.Index(s[from:], substr)
	if index < 0 {
		return -1
	}
	return from + index
}

const nameSpacer = "\r\n\t>/= "

var noChildNodes = map[string]bool{"img": true, "br": true, "input": true, "meta": true, "link": true, "hr": true}

type txmlParser struct {
	s            string
	pos          int
	keepComments bool
}

// at returns the byte at index i, or -1 past either end, as charCodeAt
// returns NaN there.
func (p *txmlParser) at(i int) int {
	if i < 0 || i >= len(p.s) {
		return -1
	}
	return int(p.s[i])
}

func (p *txmlParser) more() bool {
	return p.pos >= 0 && p.pos < len(p.s)
}

func (p *txmlParser) parseChildren(tagName string) ([]any, error) {
	children := []any{}
	for p.more() {
		if p.at(p.pos) == '<' {
			if p.at(p.pos+1) == '/' {
				closeStart := p.pos + 2
				p.pos = indexFrom(p.s, ">", p.pos)
				closeTag := jsSubstring(p.s, closeStart, p.pos)
				if !strings.Contains(closeTag, tagName) {
					parsed := strings.Split(jsSubstring(p.s, 0, p.pos), "\n")
					char := "undefined"
					if p.pos >= 0 && p.pos < len(p.s) {
						char = string(p.s[p.pos])
					}
					return nil, fmt.Errorf("Unexpected close tag\nLine: %d\nColumn: %d\nChar: %s", len(parsed)-1, len(parsed[len(parsed)-1])+1, char)
				}
				if p.pos+1 != 0 {
					p.pos++
				}
				return children, nil
			} else if p.at(p.pos+1) == '!' {
				if p.at(p.pos+2) == '-' {
					start := p.pos
					for p.pos != -1 && !(p.at(p.pos) == '>' && p.at(p.pos-1) == '-' && p.at(p.pos-2) == '-') {
						p.pos = indexFrom(p.s, ">", p.pos+1)
					}
					if p.pos == -1 {
						p.pos = len(p.s)
					}
					if p.keepComments {
						children = append(children, jsSubstring(p.s, start, p.pos+1))
					}
				} else if p.at(p.pos+2) == '[' && p.at(p.pos+8) == '[' && strings.ToLower(jsSubstring(p.s, p.pos+3, p.pos+8)) == "cdata" {
					end := indexFrom(p.s, "]]>", p.pos)
					if end == -1 {
						children = append(children, jsSubstring(p.s, p.pos+9, len(p.s)))
						p.pos = len(p.s)
					} else {
						children = append(children, jsSubstring(p.s, p.pos+9, end))
						p.pos = end + 3
					}
					continue
				} else {
					start := p.pos + 1
					p.pos += 2
					encapsulated := false
					for (p.at(p.pos) != '>' || encapsulated) && p.more() {
						if p.at(p.pos) == '[' {
							encapsulated = true
						} else if encapsulated && p.at(p.pos) == ']' {
							encapsulated = false
						}
						p.pos++
					}
					children = append(children, jsSubstring(p.s, start, p.pos))
				}
				p.pos++
				continue
			}
			child, err := p.parseNode()
			if err != nil {
				return nil, err
			}
			children = append(children, child)
		} else {
			text := p.parseText()
			if trimmed := jsTrim(text); len(trimmed) > 0 {
				children = append(children, trimmed)
			}
			p.pos++
		}
	}
	return children, nil
}

func (p *txmlParser) parseText() string {
	start := p.pos
	p.pos = indexFrom(p.s, "<", p.pos) - 1
	if p.pos == -2 {
		p.pos = len(p.s)
	}
	return jsSlice(p.s, start, p.pos+1)
}

func (p *txmlParser) parseName() string {
	start := p.pos
	for p.more() && !strings.ContainsRune(nameSpacer, rune(p.s[p.pos])) {
		p.pos++
	}
	return jsSlice(p.s, start, p.pos)
}

func isXMLSpace(c int) bool {
	return c == 32 || c == 9 || c == 10 || c == 13
}

func (p *txmlParser) parseNode() (*node, error) {
	p.pos++
	result := &node{attrs: map[string]*string{}, children: []any{}}
	result.tagName = p.parseName()
	isProcessingInstruction := strings.HasPrefix(result.tagName, "?")
	attrStart := p.pos
	for p.more() && p.at(p.pos) != '>' && !(isProcessingInstruction && p.at(p.pos) == '?' && p.at(p.pos+1) == '>') {
		c := p.at(p.pos)
		if (c > 64 && c < 91) || (c > 96 && c < 123) {
			name := p.parseName()
			var value *string
			for isXMLSpace(p.at(p.pos)) {
				p.pos++
			}
			if p.at(p.pos) == '=' {
				p.pos++
				for isXMLSpace(p.at(p.pos)) {
					p.pos++
				}
				code := p.at(p.pos)
				if code == '\'' || code == '"' {
					text := p.parseString()
					value = &text
					if p.pos == -1 {
						return result, nil
					}
				} else if code > 0 && code != '>' {
					start := p.pos
					for p.more() && !strings.ContainsRune(nameSpacer, rune(p.s[p.pos])) {
						p.pos++
					}
					text := jsSlice(p.s, start, p.pos)
					value = &text
				}
			}
			result.setAttr(name, value)
		} else {
			p.pos++
		}
	}
	if isProcessingInstruction {
		text := jsTrim(jsSlice(p.s, attrStart, p.pos))
		if len(text) > 0 && len(result.attrNames) == 0 {
			result.children = []any{text}
		}
		if p.at(p.pos) == '?' && p.at(p.pos+1) == '>' {
			p.pos += 2
		} else if p.at(p.pos) == '>' {
			p.pos++
		}
		return result, nil
	}
	if p.at(p.pos-1) != '/' && p.at(p.pos-1) != '?' {
		switch {
		case result.tagName == "script":
			start := p.pos + 1
			p.pos = indexFrom(p.s, "</script>", p.pos)
			result.children = []any{jsSlice(p.s, start, p.pos)}
			p.pos += 9
		case result.tagName == "style":
			start := p.pos + 1
			p.pos = indexFrom(p.s, "</style>", p.pos)
			result.children = []any{jsSlice(p.s, start, p.pos)}
			p.pos += 8
		case !noChildNodes[result.tagName]:
			p.pos++
			children, err := p.parseChildren(result.tagName)
			if err != nil {
				return nil, err
			}
			result.children = children
		default:
			p.pos++
		}
	} else {
		p.pos++
	}
	return result, nil
}

func (p *txmlParser) parseString() string {
	quote := p.s[p.pos : p.pos+1]
	start := p.pos + 1
	p.pos = indexFrom(p.s, quote, start)
	return jsSlice(p.s, start, p.pos)
}

// txmlParse parses a document and returns its top-level children.
func txmlParse(s string, keepComments bool) ([]any, error) {
	parser := &txmlParser{s: s, keepComments: keepComments}
	return parser.parseChildren("")
}

// txmlParseNode parses the element that starts at pos and returns it with
// the position after it.  The position is -1 when the parser stopped inside
// an attribute value that has no closing quote.
func txmlParseNode(s string, pos int) (*node, int, error) {
	parser := &txmlParser{s: s, pos: pos, keepComments: true}
	parsed, err := parser.parseNode()
	if err != nil {
		return nil, 0, err
	}
	return parsed, parser.pos, nil
}
