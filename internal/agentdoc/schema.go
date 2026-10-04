package agentdoc

import (
	"fmt"
	"strings"

	"github.com/elqsar/better-api-portal/internal/schematree"
)

// maxSchemaLines caps one schema's field list, so a huge schema can't
// crowd everything else out of an agent's context.
const maxSchemaLines = 200

// writeSchema writes the schema at pointer ptr as a nested field list,
// one property per line with its type, whether it's required, the $ref it
// came from, its description and constraints:
// "- `total` (object, required, Money): Including tax.".
func (w *writer) writeSchema(v *Version, ptr string) {
	if ptr == "" {
		w.line("No schema.")
		return
	}
	root, err := schematree.Build(v.schema(ptr))
	if err != nil {
		w.line("The schema can't be shown: " + err.Error() + ".")
		return
	}
	sw := &schemaWriter{w: w}
	// An object's fields are the list; the object itself gets a line only
	// if there's something to say about it.
	if head := nodeHead(root); root.Type != "object" || len(root.Children) == 0 || hasDetail(root) {
		w.line(strings.TrimPrefix(head, "- "))
	}
	for _, c := range root.Children {
		sw.node(c, 0)
	}
	if sw.dropped > 0 {
		w.linef("- … %d more lines not shown: the schema is too large.", sw.dropped)
	}
}

type schemaWriter struct {
	w       *writer
	lines   int
	dropped int
}

func (sw *schemaWriter) node(n *schematree.Node, depth int) {
	if sw.lines == maxSchemaLines {
		sw.dropped++
	} else {
		sw.lines++
		sw.w.line(strings.Repeat("  ", depth) + nodeHead(n))
	}
	for _, c := range n.Children {
		sw.node(c, depth+1)
	}
}

// hasDetail reports whether a node says more than its type.
func hasDetail(n *schematree.Node) bool {
	return n.Description != "" || len(n.Facts) > 0 || len(n.Enum) > 0 || n.Note != "" || n.Ref != ""
}

// nodeHead is a node's list item, without its children.
func nodeHead(n *schematree.Node) string {
	var b strings.Builder
	b.WriteString("- ")
	switch {
	case n.Group:
		b.WriteString(n.Name + ":")
		return b.String()
	case n.Name == "":
	case n.Label:
		b.WriteString(n.Name + " ")
	default:
		b.WriteString("`" + n.Name + "` ")
	}
	var tags []string
	if n.Type != "" {
		tags = append(tags, n.Type)
	}
	if n.Required {
		tags = append(tags, "required")
	}
	if n.Ref != "" {
		tags = append(tags, refName(n.Ref))
	}
	if len(tags) > 0 {
		b.WriteString("(" + strings.Join(tags, ", ") + ")")
	}
	var rest []string
	if n.Description != "" {
		rest = append(rest, sentence(oneLine(n.Description)))
	}
	if len(n.Enum) > 0 {
		rest = append(rest, "One of "+strings.Join(n.Enum, ", ")+".")
	}
	if len(n.Facts) > 0 {
		rest = append(rest, strings.Join(n.Facts, "; ")+".")
	}
	if n.Note != "" {
		rest = append(rest, fmt.Sprintf("(%s)", n.Note))
	}
	if len(rest) > 0 {
		b.WriteString(": " + strings.Join(rest, " "))
	}
	return strings.TrimRight(b.String(), " ")
}

// oneLine joins a multi-line text into one line, for list items.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// sentence ends s with a full stop unless it already ends a sentence.
func sentence(s string) string {
	if s == "" || strings.ContainsAny(s[len(s)-1:], ".!?:") {
		return s
	}
	return s + "."
}
