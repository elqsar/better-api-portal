// Package yamldoc parses YAML (and JSON) documents while keeping the source
// line of every JSON pointer, and validates them against JSON Schemas.
package yamldoc

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Doc is a parsed document.
type Doc struct {
	Path  string
	Root  *yaml.Node
	lines map[string]int
}

// SyntaxError is a YAML syntax error; Line is 0 when unknown.
type SyntaxError struct {
	Line int
	Err  error
}

func (e *SyntaxError) Error() string { return e.Err.Error() }
func (e *SyntaxError) Unwrap() error { return e.Err }

// Parse parses b, read from path. A malformed document yields a *SyntaxError.
func Parse(path string, b []byte) (*Doc, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, &SyntaxError{Line: ErrorLine(err), Err: err}
	}
	d := &Doc{Path: path, Root: &root, lines: map[string]int{}}
	d.index(&root, "")
	return d, nil
}

func (d *Doc) index(n *yaml.Node, ptr string) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			d.index(c, ptr)
		}
		return
	case yaml.AliasNode:
		d.index(n.Alias, ptr)
		return
	}
	d.lines[ptr] = n.Line
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := ptr + "/" + escape(k.Value)
			d.index(v, p)
			d.lines[p] = k.Line // point at the key, which is where readers look
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			d.index(c, ptr+"/"+strconv.Itoa(i))
		}
	}
}

// Line returns the line of ptr, or of its nearest ancestor that has one.
func (d *Doc) Line(ptr string) int {
	for {
		if n, ok := d.lines[ptr]; ok {
			return n
		}
		i := strings.LastIndexByte(ptr, '/')
		if i < 0 {
			return 0
		}
		ptr = ptr[:i]
	}
}

// Decode decodes the document into v, as yaml.Unmarshal would.
func (d *Doc) Decode(v any) error {
	if err := d.Root.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", d.Path, err)
	}
	return nil
}

// JSON converts the document into the JSON data model schema validators
// expect.
func (d *Doc) JSON() any { return toJSON(d.Root) }

// Pointer builds a JSON pointer from unescaped tokens.
func Pointer(tokens ...string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escape(t))
	}
	return b.String()
}

func escape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// toJSON converts a YAML node to maps, slices and scalars. Timestamps and
// other non-JSON scalars stay strings, as they would in a JSON document.
func toJSON(n *yaml.Node) any {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return toJSON(n.Content[0])
	case yaml.AliasNode:
		return toJSON(n.Alias)
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			m[n.Content[i].Value] = toJSON(n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		s := make([]any, len(n.Content))
		for i, c := range n.Content {
			s[i] = toJSON(c)
		}
		return s
	}
	switch n.ShortTag() {
	case "!!null":
		return nil
	case "!!bool":
		var b bool
		if n.Decode(&b) == nil {
			return b
		}
	case "!!int":
		var i int64
		if n.Decode(&i) == nil {
			return i
		}
	case "!!float":
		var f float64
		if n.Decode(&f) == nil {
			return f
		}
	}
	return n.Value
}

var lineRe = regexp.MustCompile(`line (\d+)`)

// ErrorLine extracts the line number from a yaml syntax error, or 0.
func ErrorLine(err error) int {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		return 0
	}
	if m := lineRe.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}
