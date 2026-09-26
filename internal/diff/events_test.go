package diff

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"better-api-portal/internal/compat"
	"better-api-portal/internal/model"
	"better-api-portal/internal/spec/eventcatalog"
)

// base is one message using every attribute the diff looks at.
const base = `eventcatalog: "1.0"
title: T
version: 1.0.0
messages:
  - type: com.acme.t.happened.v1
    role: produces
    summary: S
    source: /svc
    subject: t/{id}
    datacontenttype: application/json
    dataschema: {schema: {type: object, properties: {id: {type: string}}}}
    extensions: {partitionkey: {required: true, type: string}, traceparent: {type: string}}
    bindings: [{kafka: {topic: t.events, key: {from: subject}, mode: binary}}]
`

const second = `  - type: com.acme.t.other.v1
    role: produces
    summary: S
    dataschema: {schema: {}}
    bindings: [{sqs: {queue: q}}]
`

func parse(t *testing.T, dir, events string) *eventcatalog.Result {
	t.Helper()
	p := filepath.Join(dir, "events.yaml")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	res, fs, err := eventcatalog.Parse(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) > 0 {
		t.Fatalf("fixture doesn't parse cleanly: %+v\n%s", fs, events)
	}
	return res
}

func replace(t *testing.T, s string, pairs [][2]string) string {
	t.Helper()
	for _, p := range pairs {
		if !strings.Contains(s, p[0]) {
			t.Fatalf("fixture has no %q", p[0])
		}
		s = strings.Replace(s, p[0], p[1], 1)
	}
	return s
}

var receives = [][2]string{{"role: produces", "role: receives"}}

type diffCase struct {
	name      string
	both      [][2]string // applied to old and new
	edit      [][2]string // applied to new only
	oldAppend string
	newAppend string
	override  compat.Mode
	want      []string // "rule impact", in any order
}

var diffCases = []diffCase{
	{name: "identical"},
	{name: "message type removed", oldAppend: second, want: []string{"ce-message-removed breaking"}},
	{name: "message type added", newAppend: second, want: []string{"ce-message-added additive"}},
	{name: "role changed", edit: receives, want: []string{"ce-role-changed breaking"}},

	{name: "payload type changed", edit: [][2]string{{"id: {type: string}", "id: {type: integer}"}},
		want: []string{"compat-type breaking"}},
	{name: "payload changed compatibly", edit: [][2]string{{"id: {type: string}", "id: {type: string}, n: {type: integer}"}},
		want: []string{"ce-payload-changed additive"}},
	{name: "payload docs changed", edit: [][2]string{{"id: {type: string}", "id: {type: string, description: The id.}"}},
		want: []string{"ce-docs-changed docs"}},
	{name: "payload change under the descriptor's FULL override",
		edit: [][2]string{{"properties: {id", "required: [id], properties: {id"}}, override: compat.Full,
		want: []string{"compat-required breaking"}},
	{name: "the same change under the default (FORWARD for produces)",
		edit: [][2]string{{"properties: {id", "required: [id], properties: {id"}},
		want: []string{"ce-payload-changed additive"}},

	{name: "binding address changed", edit: [][2]string{{"topic: t.events", "topic: t.other"}},
		want: []string{"ce-binding-added additive", "ce-binding-removed breaking"}},
	{name: "binding added", edit: [][2]string{{"mode: binary}}]", "mode: binary}}, {nats: {subject: t.x}}]"}},
		want: []string{"ce-binding-added additive"}},
	{name: "kafka key changed", edit: [][2]string{{"key: {from: subject}", "key: {from: data, pointer: /id}"}},
		want: []string{"ce-binding-changed breaking"}},
	{name: "kafka mode changed", edit: [][2]string{{"mode: binary", "mode: structured"}},
		want: []string{"ce-binding-changed breaking"}},
	{name: "datacontenttype changed", edit: [][2]string{{"datacontenttype: application/json", "datacontenttype: application/cloudevents+json"}},
		want: []string{"ce-datacontenttype-changed breaking"}},

	{name: "required extension added (produces)", edit: [][2]string{{"extensions: {", "extensions: {tenant: {required: true}, "}},
		want: []string{"ce-extension-required-added additive"}},
	{name: "required extension added (receives)", both: receives, edit: [][2]string{{"extensions: {", "extensions: {tenant: {required: true}, "}},
		want: []string{"ce-extension-required-added breaking"}},
	{name: "extension made required (produces)", edit: [][2]string{{"traceparent: {type", "traceparent: {required: true, type"}},
		want: []string{"ce-extension-required-added additive"}},
	{name: "extension made required (receives)", both: receives, edit: [][2]string{{"traceparent: {type", "traceparent: {required: true, type"}},
		want: []string{"ce-extension-required-added breaking"}},
	{name: "required extension removed (produces)", edit: [][2]string{{"partitionkey: {required: true, type: string}, ", ""}},
		want: []string{"ce-extension-required-removed breaking"}},
	{name: "required extension removed (receives)", both: receives, edit: [][2]string{{"partitionkey: {required: true, type: string}, ", ""}},
		want: []string{"ce-extension-required-removed additive"}},
	{name: "extension made optional (produces)", edit: [][2]string{{"required: true, type: string}", "required: false, type: string}"}},
		want: []string{"ce-extension-required-removed breaking"}},
	{name: "extension type changed", edit: [][2]string{{"partitionkey: {required: true, type: string}", "partitionkey: {required: true, type: integer}"}},
		want: []string{"ce-extension-type-changed breaking"}},
	{name: "optional extension removed", edit: [][2]string{{", traceparent: {type: string}", ""}},
		want: []string{"ce-extension-changed additive"}},

	{name: "source changed", edit: [][2]string{{"source: /svc", "source: /svc/{region}"}}, want: []string{"ce-source-changed warn"}},
	{name: "subject changed", edit: [][2]string{{"subject: t/{id}", "subject: things/{id}"}}, want: []string{"ce-subject-changed warn"}},
	{name: "dataschemauri set", edit: [][2]string{{"    datacontenttype", "    dataschemauri: https://schemas.acme.test/happened/v1\n    datacontenttype"}},
		want: []string{"ce-dataschemauri-changed warn"}},
	{name: "deprecated", edit: [][2]string{{"summary: S", "summary: S\n    deprecated: true"}}, want: []string{"ce-message-deprecated additive"}},
	{name: "summary changed", edit: [][2]string{{"summary: S", "summary: Something happened."}}, want: []string{"ce-docs-changed docs"}},
	{name: "catalogue title changed", edit: [][2]string{{"title: T", "title: Things"}}, want: []string{"ce-docs-changed docs"}},
}

func TestEvents(t *testing.T) {
	for _, tc := range diffCases {
		t.Run(tc.name, func(t *testing.T) {
			changes := run(t, tc)
			var got []string
			for _, c := range changes {
				got = append(got, c.RuleID+" "+string(c.Impact))
				if (c.Impact == model.ImpactBreaking) != (c.ID != "") {
					t.Errorf("%s: id %q, but only breaking changes have one", c.RuleID, c.ID)
				}
				if c.File == "" || c.Line == 0 {
					t.Errorf("%s: not located: %+v", c.RuleID, c)
				}
			}
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("changes = %q, want %q\n%+v", got, want, changes)
			}
		})
	}
}

func run(t *testing.T, tc diffCase) []model.Change {
	t.Helper()
	oldY := replace(t, base, tc.both) + tc.oldAppend
	newY := replace(t, replace(t, base, tc.both), tc.edit) + tc.newAppend
	root := t.TempDir()
	old := parse(t, filepath.Join(root, "old"), oldY)
	new := parse(t, filepath.Join(root, "new"), newY)
	changes, err := Events(old, new, tc.override)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func TestPayloadChangeNamesTheField(t *testing.T) {
	changes := run(t, diffCase{edit: [][2]string{{"id: {type: string}", "id: {type: integer}"}}})
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	c := changes[0]
	if c.Field != "/id" || c.Type != "com.acme.t.happened.v1" || c.Pointer != "/messages/0/dataschema" ||
		!strings.Contains(c.Message, "payload /id: type changed from string to integer (breaks FORWARD: consumers on the old schema can't read new events)") {
		t.Errorf("change = %+v", c)
	}
}

func TestChangeIDsAreStable(t *testing.T) {
	tc := diffCase{edit: [][2]string{{"id: {type: string}", "id: {type: integer}"}, {"topic: t.events", "topic: t.other"}}}
	ids := func() []string {
		var out []string
		for _, c := range run(t, tc) {
			if c.ID != "" {
				out = append(out, c.ID)
			}
		}
		return out
	}
	first, again := ids(), ids()
	if len(first) != 2 || !slices.Equal(first, again) {
		t.Fatalf("ids = %v then %v", first, again)
	}
	if first[0] == first[1] || !strings.HasPrefix(first[0], "BRK-CE-") || len(first[0]) != len("BRK-CE-")+6 {
		t.Errorf("ids = %v", first)
	}
}
