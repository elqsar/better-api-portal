package compat

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"better-api-portal/internal/spec/eventcatalog"
	"better-api-portal/internal/yamldoc"
)

var update = flag.Bool("update", false, "rewrite golden files")

// parse decodes JSON the way the parser does (via yamldoc), so numbers have
// the same Go types as in real bundles.
func parse(t *testing.T, s string) any {
	t.Helper()
	d, err := yamldoc.Parse("test.json", []byte(s))
	if err != nil {
		t.Fatalf("bad fixture %s: %v", s, err)
	}
	return d.JSON()
}

// bundle builds a Schema from files; the first file is the root unless root
// is set.
type bundle struct {
	files [][2]string // name, JSON
	root  string
}

func one(js string) bundle { return bundle{files: [][2]string{{"api/s.json", js}}} }

func (b bundle) schema(t *testing.T) Schema {
	s := Schema{Docs: map[string]any{}, Root: b.root}
	for _, f := range b.files {
		s.Docs[f[0]] = parse(t, f[1])
	}
	if s.Root == "" {
		s.Root = b.files[0][0]
	}
	return s
}

type verdict map[Mode]string // mode → sorted, comma-joined rule ids; "" is compatible

type compatCase struct {
	name     string
	table    bool // a row (or * variant) of the table in 04-governance §2
	old, new bundle
	want     verdict
	// paths optionally pins the issue paths per mode, comma-joined.
	paths map[Mode]string
}

const (
	ok  = ""
	typ = RuleType
	enm = RuleEnum
	req = RuleRequired
	add = RuleAdditionalProperties
	con = RuleConstraint
	unv = RuleUnverified
)

var cases = []compatCase{
	// --- 04-governance §2, "Change to the payload schema" table, row by row.
	{
		name: "Add an optional property", table: true,
		old:  one(`{"type": "object", "properties": {"a": {"type": "string"}}}`),
		new:  one(`{"type": "object", "properties": {"a": {"type": "string"}, "b": {"type": "string"}}}`),
		want: verdict{Forward: ok, Backward: ok, Full: ok},
	},
	{
		name: "Add an optional property * old side closed", table: true,
		old:  one(`{"type": "object", "additionalProperties": false, "properties": {"a": {"type": "string"}}}`),
		new:  one(`{"type": "object", "additionalProperties": false, "properties": {"a": {"type": "string"}, "b": {"type": "string"}}}`),
		want: verdict{Forward: add, Backward: ok, Full: add},
	},
	{
		name: "Add a required property", table: true,
		old:  one(`{"type": "object", "properties": {"a": {"type": "string"}}}`),
		new:  one(`{"type": "object", "required": ["b"], "properties": {"a": {"type": "string"}, "b": {"type": "string"}}}`),
		want: verdict{Forward: ok, Backward: req, Full: req},
	},
	{
		name: "Add a required property * old side closed", table: true,
		old:  one(`{"type": "object", "additionalProperties": false, "properties": {"a": {"type": "string"}}}`),
		new:  one(`{"type": "object", "additionalProperties": false, "required": ["b"], "properties": {"a": {"type": "string"}, "b": {"type": "string"}}}`),
		want: verdict{Forward: add, Backward: req, Full: add + "," + req},
	},
	{
		name: "Remove a property from required", table: true,
		old:  one(`{"type": "object", "required": ["a"], "properties": {"a": {"type": "string"}}}`),
		new:  one(`{"type": "object", "properties": {"a": {"type": "string"}}}`),
		want: verdict{Forward: req, Backward: ok, Full: req},
	},
	{
		name: "Remove a property that was optional", table: true,
		old:  one(`{"type": "object", "properties": {"a": {"type": "string"}, "b": {"type": "string"}}}`),
		new:  one(`{"type": "object", "properties": {"a": {"type": "string"}}}`),
		want: verdict{Forward: ok, Backward: ok, Full: ok},
	},
	{
		name: "Remove a property that was required", table: true,
		old:  one(`{"type": "object", "required": ["b"], "properties": {"a": {"type": "string"}, "b": {"type": "string"}}}`),
		new:  one(`{"type": "object", "properties": {"a": {"type": "string"}}}`),
		want: verdict{Forward: req, Backward: ok, Full: req},
	},
	{
		name: "Remove a property * new side closed", table: true,
		old:  one(`{"type": "object", "additionalProperties": false, "properties": {"a": {"type": "string"}, "b": {"type": "string"}}}`),
		new:  one(`{"type": "object", "additionalProperties": false, "properties": {"a": {"type": "string"}}}`),
		want: verdict{Forward: ok, Backward: add, Full: add},
	},
	{
		name: "Widen an enum", table: true,
		old:  one(`{"type": "string", "enum": ["a", "b"]}`),
		new:  one(`{"type": "string", "enum": ["a", "b", "c"]}`),
		want: verdict{Forward: enm, Backward: ok, Full: enm},
	},
	{
		name: "Narrow an enum", table: true,
		old:  one(`{"type": "string", "enum": ["a", "b", "c"]}`),
		new:  one(`{"type": "string", "enum": ["a", "b"]}`),
		want: verdict{Forward: ok, Backward: enm, Full: enm},
	},
	{
		name: "Change a type", table: true,
		old:  one(`{"type": "object", "properties": {"n": {"type": "string"}}}`),
		new:  one(`{"type": "object", "properties": {"n": {"type": "integer"}}}`),
		want: verdict{Forward: typ, Backward: typ, Full: typ},
		// Reported once in FULL: the same change breaks both guarantees.
		paths: map[Mode]string{Full: "/n"},
	},
	{
		name: "Tighten a constraint (lower maxLength)", table: true,
		old:  one(`{"type": "string", "maxLength": 50}`),
		new:  one(`{"type": "string", "maxLength": 20}`),
		want: verdict{Forward: ok, Backward: con, Full: con},
	},
	{
		name: "Loosen a constraint (raise maxLength)", table: true,
		old:  one(`{"type": "string", "maxLength": 20}`),
		new:  one(`{"type": "string", "maxLength": 50}`),
		want: verdict{Forward: con, Backward: ok, Full: con},
	},
	{
		name: "Any change involving keywords outside the subset", table: true,
		old:  one(`{"oneOf": [{"type": "string"}, {"type": "integer"}]}`),
		new:  one(`{"oneOf": [{"type": "string"}, {"type": "boolean"}]}`),
		want: verdict{Forward: unv, Backward: unv, Full: unv},
	},

	// --- Beyond the table.
	{
		name: "integer to number",
		old:  one(`{"type": "integer"}`),
		new:  one(`{"type": "number"}`),
		want: verdict{Forward: typ, Backward: ok},
	},
	{
		name: "number to integer",
		old:  one(`{"type": "number"}`),
		new:  one(`{"type": "integer"}`),
		want: verdict{Forward: ok, Backward: typ},
	},
	{
		name: "type becomes nullable",
		old:  one(`{"type": "string"}`),
		new:  one(`{"type": ["string", "null"]}`),
		want: verdict{Forward: typ, Backward: ok},
	},
	{
		name: "const to enum",
		old:  one(`{"const": "a"}`),
		new:  one(`{"enum": ["a", "b"]}`),
		want: verdict{Forward: enm, Backward: ok},
	},
	{
		name: "untyped enum gains a type of the same kind",
		old:  one(`{"enum": ["a", "b"]}`),
		new:  one(`{"type": "string", "enum": ["a", "b"]}`),
		want: verdict{Forward: ok, Backward: ok},
	},
	{
		name: "enum removed",
		old:  one(`{"type": "string", "enum": ["a"]}`),
		new:  one(`{"type": "string"}`),
		want: verdict{Forward: enm, Backward: ok},
	},
	{
		name: "numeric enum values compare by value",
		old:  one(`{"enum": [1, 2]}`),
		new:  one(`{"enum": [1.0, 2]}`),
		want: verdict{Full: ok},
	},
	{
		name:  "nested items change",
		old:   one(`{"type": "object", "properties": {"lines": {"type": "array", "items": {"type": "object", "properties": {"quantity": {"type": "integer", "minimum": 1}}}}}}`),
		new:   one(`{"type": "object", "properties": {"lines": {"type": "array", "items": {"type": "object", "properties": {"quantity": {"type": "integer", "minimum": 0}}}}}}`),
		want:  verdict{Forward: con, Backward: ok},
		paths: map[Mode]string{Forward: "/lines/[]/quantity"},
	},
	{
		name: "items schema removed",
		old:  one(`{"type": "array", "items": {"type": "string"}}`),
		new:  one(`{"type": "array"}`),
		want: verdict{Forward: con, Backward: ok},
	},
	{
		name: "additionalProperties closed in new",
		old:  one(`{"type": "object", "properties": {"a": {"type": "string"}}}`),
		new:  one(`{"type": "object", "additionalProperties": false, "properties": {"a": {"type": "string"}}}`),
		want: verdict{Forward: ok, Backward: add},
	},
	{
		name:  "additionalProperties schema narrowed",
		old:   one(`{"type": "object", "additionalProperties": {"type": ["string", "integer"]}}`),
		new:   one(`{"type": "object", "additionalProperties": {"type": "string"}}`),
		want:  verdict{Forward: ok, Backward: typ},
		paths: map[Mode]string{Backward: "/*"},
	},
	{
		name: "pattern added",
		old:  one(`{"type": "string"}`),
		new:  one(`{"type": "string", "pattern": "^[A-Z]{3}$"}`),
		want: verdict{Forward: ok, Backward: con},
	},
	{
		name: "pattern changed",
		old:  one(`{"type": "string", "pattern": "^[A-Z]{3}$"}`),
		new:  one(`{"type": "string", "pattern": "^[A-Z]{2,3}$"}`),
		want: verdict{Forward: con, Backward: con},
	},
	{
		name: "only annotations change",
		old:  one(`{"type": "string", "description": "old", "examples": ["x"]}`),
		new:  one(`{"type": "string", "description": "new", "title": "T", "deprecated": true}`),
		want: verdict{Full: ok},
	},
	{
		name: "unsupported keyword unchanged",
		old:  one(`{"type": "object", "properties": {"v": {"anyOf": [{"type": "string"}, {"type": "integer"}]}, "a": {"type": "string"}}}`),
		new:  one(`{"type": "object", "properties": {"v": {"anyOf": [{"type": "string"}, {"type": "integer"}]}, "a": {"type": "string"}, "b": {"type": "string"}}}`),
		want: verdict{Full: ok},
	},
	{
		name:  "unsupported keyword changed gives one issue",
		old:   one(`{"type": "object", "properties": {"v": {"type": "array", "minItems": 1, "items": {"type": "string"}}}}`),
		new:   one(`{"type": "object", "properties": {"v": {"type": "array", "minItems": 2, "items": {"type": "integer"}}}}`),
		want:  verdict{Forward: unv, Backward: unv},
		paths: map[Mode]string{Forward: "/v"},
	},
	{
		name: "ref with annotation sibling is followed",
		old:  bundle{files: [][2]string{{"api/s.json", `{"$ref": "#/$defs/A", "description": "d", "$defs": {"A": {"type": "string"}}}`}}},
		new:  bundle{files: [][2]string{{"api/s.json", `{"$ref": "#/$defs/A", "description": "e", "$defs": {"A": {"type": "integer"}}}`}}},
		want: verdict{Forward: typ, Backward: typ},
	},
	{
		name: "ref with constraint sibling is unverified",
		old:  one(`{"$ref": "#/$defs/A", "$defs": {"A": {"type": "string"}}}`),
		new:  one(`{"$ref": "#/$defs/A", "minLength": 1, "$defs": {"A": {"type": "string"}}}`),
		want: verdict{Forward: unv, Backward: unv},
	},
	{
		name: "cross-file ref",
		old: bundle{files: [][2]string{
			{"api/schemas/order.json", `{"type": "object", "properties": {"total": {"$ref": "./money.json"}}}`},
			{"api/schemas/money.json", `{"type": "object", "properties": {"amount": {"type": "integer"}}}`},
		}},
		new: bundle{files: [][2]string{
			{"api/schemas/order.json", `{"type": "object", "properties": {"total": {"$ref": "./money.json"}}}`},
			{"api/schemas/money.json", `{"type": "object", "properties": {"amount": {"type": "string"}}}`},
		}},
		want:  verdict{Forward: typ, Backward: typ},
		paths: map[Mode]string{Forward: "/total/amount"},
	},
	{
		name: "ref inside an inline schema",
		old: bundle{files: [][2]string{
			{"api/events.yaml#/messages/0/dataschema/schema", `{"properties": {"m": {"$ref": "#/$defs/M"}, "c": {"$ref": "./c.json"}}, "$defs": {"M": {"type": "integer"}}}`},
			{"api/c.json", `{"type": "string"}`},
		}},
		new: bundle{files: [][2]string{
			{"api/events.yaml#/messages/0/dataschema/schema", `{"properties": {"m": {"$ref": "#/$defs/M"}, "c": {"$ref": "./c.json"}}, "$defs": {"M": {"type": "integer", "minimum": 0}}}`},
			{"api/c.json", `{"type": "string", "maxLength": 3}`},
		}},
		want:  verdict{Forward: ok, Backward: con},
		paths: map[Mode]string{Backward: "/c,/m"},
	},
	{
		name: "fragment root",
		old:  bundle{files: [][2]string{{"api/a.json", `{"$defs": {"E": {"type": "string"}}}`}}, root: "api/a.json#/$defs/E"},
		new:  bundle{files: [][2]string{{"api/a.json", `{"$defs": {"E": {"type": "string", "enum": ["x"]}}}`}}, root: "api/a.json#/$defs/E"},
		want: verdict{Forward: ok, Backward: enm},
	},
	{
		name:  "recursive schema",
		old:   one(`{"$ref": "#/$defs/Node", "$defs": {"Node": {"type": "object", "properties": {"name": {"type": "string"}, "children": {"type": "array", "items": {"$ref": "#/$defs/Node"}}}}}}`),
		new:   one(`{"$ref": "#/$defs/Node", "$defs": {"Node": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}, "children": {"type": "array", "items": {"$ref": "#/$defs/Node"}}}}}}`),
		want:  verdict{Forward: ok, Backward: req},
		paths: map[Mode]string{Backward: "/name"},
	},
	{
		name: "ref to a missing definition",
		old:  one(`{"$ref": "#/$defs/A", "$defs": {"A": {}}}`),
		new:  one(`{"$ref": "#/$defs/B", "$defs": {"A": {}}}`),
		want: verdict{Forward: unv, Backward: unv},
	},
	{
		name: "true schema to typed",
		old:  one(`true`),
		new:  one(`{"type": "string"}`),
		want: verdict{Forward: ok, Backward: typ},
	},
	{
		name: "false schema to anything",
		old:  one(`false`),
		new:  one(`{"type": "string"}`),
		want: verdict{Forward: typ, Backward: ok},
	},
	{
		name: "NONE checks nothing",
		old:  one(`{"type": "string"}`),
		new:  one(`{"type": "integer"}`),
		want: verdict{None: ok},
	},
}

func rules(issues []Issue) string {
	var rs []string
	for _, is := range issues {
		if !slices.Contains(rs, is.Rule) {
			rs = append(rs, is.Rule)
		}
	}
	slices.Sort(rs)
	return strings.Join(rs, ",")
}

func paths(issues []Issue) string {
	var ps []string
	for _, is := range issues {
		ps = append(ps, is.Path)
	}
	return strings.Join(ps, ",")
}

func runCase(t *testing.T, tc compatCase) {
	old, new := tc.old.schema(t), tc.new.schema(t)
	for _, m := range []Mode{Forward, Backward, Full, None} {
		want, ok := tc.want[m]
		if !ok {
			continue
		}
		issues, err := Check(old, new, m)
		if err != nil {
			t.Fatal(err)
		}
		if got := rules(issues); got != want {
			t.Errorf("%s: rules = %q, want %q\n%v", m, got, want, issues)
		}
		if want, ok := tc.paths[m]; ok {
			if got := paths(issues); got != want {
				t.Errorf("%s: paths = %q, want %q", m, got, want)
			}
		}
	}
}

// TestGovernanceTable encodes each row of the table in 04-governance §2.
func TestGovernanceTable(t *testing.T) {
	for _, tc := range cases {
		if tc.table {
			t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
		}
	}
}

func TestBeyondTable(t *testing.T) {
	for _, tc := range cases {
		if !tc.table {
			t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
		}
	}
}

// TestGolden renders every case in every mode, so the wording of all issues
// can be reviewed in one place.
func TestGolden(t *testing.T) {
	var b strings.Builder
	for _, tc := range cases {
		fmt.Fprintf(&b, "== %s\n", tc.name)
		old, new := tc.old.schema(t), tc.new.schema(t)
		for _, m := range []Mode{Forward, Backward, Full} {
			issues, err := Check(old, new, m)
			if err != nil {
				t.Fatal(err)
			}
			if len(issues) == 0 {
				fmt.Fprintf(&b, "%-8s  ok\n", m)
			}
			for _, is := range issues {
				p := is.Path
				if p == "" {
					p = "(root)"
				}
				fmt.Fprintf(&b, "%-8s  %-8s  %-28s  %s: %s\n", m, is.Direction, is.Rule, p, is.Message)
			}
		}
	}
	got := b.String()
	golden := "testdata/compat.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s (run go test -update to accept):\n%s", golden, got)
	}
}

func TestCheckMissingRoot(t *testing.T) {
	s := Schema{Docs: map[string]any{}, Root: "api/nope.json"}
	if _, err := Check(s, s, Full); err == nil {
		t.Fatal("expected an error")
	}
}

const example = "../../docs/spec/examples/orders-service"

func TestExampleAgainstItself(t *testing.T) {
	res, fs, err := eventcatalog.Parse(example, filepath.Join(example, "api/events.yaml"))
	if err != nil || len(fs) > 0 {
		t.Fatalf("parse: %v %+v", err, fs)
	}
	for _, m := range res.Spec.Messages {
		s := FromSpec(res.Spec, m.Payload)
		issues, err := Check(s, s, Full)
		if err != nil {
			t.Fatal(err)
		}
		if len(issues) > 0 {
			t.Errorf("%s against itself: %v", m.Key, issues)
		}
	}
}

func TestExampleModified(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	edit := func(name, old, new string) {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Replace(string(b), old, new, 1)
		if s == string(b) {
			t.Fatalf("%s: %q not found", name, old)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edit("api/schemas/order-created.v1.json", `"required": ["orderId", "customerId", "total"]`, `"required": ["orderId", "total"]`)
	edit("api/schemas/money.json", `"^[A-Z]{3}$"`, `"^[A-Za-z]{3}$"`)

	parse := func(root string) *eventcatalog.Result {
		res, fs, err := eventcatalog.Parse(root, filepath.Join(root, "api/events.yaml"))
		if err != nil || len(fs) > 0 {
			t.Fatalf("parse %s: %v %+v", root, err, fs)
		}
		return res
	}
	before, after := parse(example), parse(dir)
	const created = "api/schemas/order-created.v1.json"
	issues, err := Check(FromSpec(before.Spec, created), FromSpec(after.Spec, created), DefaultMode(before.Spec.Messages[0].Role))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, is := range issues {
		got = append(got, is.Rule+" "+is.Path)
	}
	want := []string{"compat-required /customerId", "compat-constraint /total/currency"}
	if !slices.Equal(got, want) {
		t.Errorf("issues = %q, want %q", got, want)
	}
}

func TestFingerprint(t *testing.T) {
	fp := func(b bundle, annotations bool) string {
		t.Helper()
		s, err := Fingerprint(b.schema(t), annotations)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	plain := one(`{"type": "object", "properties": {"a": {"type": "string"}}}`)
	described := one(`{"type": "object", "description": "d", "properties": {"a": {"type": "string", "title": "A"}}}`)
	constrained := one(`{"type": "object", "properties": {"a": {"type": "string", "maxLength": 3}}}`)
	// A property named like an annotation is data, not an annotation.
	namedDescription := one(`{"type": "object", "properties": {"description": {"type": "string"}}}`)

	if fp(plain, false) != fp(described, false) {
		t.Error("annotations changed the constraints fingerprint")
	}
	if fp(plain, true) == fp(described, true) {
		t.Error("annotations didn't change the full fingerprint")
	}
	if fp(plain, false) == fp(constrained, false) {
		t.Error("a constraint didn't change the fingerprint")
	}
	if !strings.Contains(fp(namedDescription, false), `"description"`) {
		t.Error("a property named description was dropped")
	}

	// $refs are inlined, so the same schema split across files matches.
	split := bundle{files: [][2]string{
		{"api/s.json", `{"type": "object", "properties": {"a": {"$ref": "./a.json"}}}`},
		{"api/a.json", `{"type": "string"}`},
	}}
	inlined := one(`{"type": "object", "properties": {"a": {"$ref": "#/$defs/A"}}, "$defs": {"A": {"type": "string"}}}`)
	if fp(split, false) != fp(inlined, false) {
		t.Errorf("same schema, different layout:\n%s\n%s", fp(split, false), fp(inlined, false))
	}

	// Recursive schemas terminate.
	tree := one(`{"$ref": "#/$defs/N", "$defs": {"N": {"type": "object", "properties": {"kids": {"type": "array", "items": {"$ref": "#/$defs/N"}}}}}}`)
	if !strings.Contains(fp(tree, false), "$cycle") {
		t.Error("cycle not marked")
	}
}
