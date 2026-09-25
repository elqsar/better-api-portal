package descriptor

import (
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// lines maps JSON pointers to the line they start on in the YAML source.
type lines map[string]int

func indexLines(n *yaml.Node) lines {
	l := lines{}
	var walk func(n *yaml.Node, ptr string)
	walk = func(n *yaml.Node, ptr string) {
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, ptr)
			}
			return
		case yaml.AliasNode:
			walk(n.Alias, ptr)
			return
		}
		l[ptr] = n.Line
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				p := ptr + "/" + escapePointer(k.Value)
				walk(v, p)
				l[p] = k.Line // point at the key, which is where readers look
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				walk(c, ptr+"/"+strconv.Itoa(i))
			}
		}
	}
	walk(n, "")
	return l
}

// lookup returns the line of ptr, or of its nearest ancestor that has one.
func (l lines) lookup(ptr string) int {
	for {
		if n, ok := l[ptr]; ok {
			return n
		}
		i := strings.LastIndexByte(ptr, '/')
		if i < 0 {
			return 0
		}
		ptr = ptr[:i]
	}
}

func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func pointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escapePointer(t))
	}
	return b.String()
}

// toJSON converts a YAML node into the JSON data model the schema validator
// expects. Timestamps and other non-JSON scalars stay strings, as they would
// in a JSON document.
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
