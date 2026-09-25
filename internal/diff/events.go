// Package diff compares two versions of an API contract and classifies each
// change by its impact (docs/spec/04-governance.md §2).
package diff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"better-api-portal/internal/compat"
	"better-api-portal/internal/model"
	"better-api-portal/internal/spec/eventcatalog"
)

// Events compares two versions of an event catalogue. Messages are matched
// by type. Payloads are compared under override if set (the descriptor's
// compatibility), else under the default for each message's role.
func Events(old, new *eventcatalog.Result, override compat.Mode) ([]model.Change, error) {
	d := &eventDiff{old: old, new: new, override: override}
	if old.Spec.Title != new.Spec.Title {
		d.addNew(model.Change{RuleID: "ce-docs-changed", Kind: "changed", Target: "metadata", Impact: model.ImpactDocs,
			Message: fmt.Sprintf("title changed from %q to %q", old.Spec.Title, new.Spec.Title)}, "/title")
	}
	if old.Spec.Description != new.Spec.Description {
		d.addNew(model.Change{RuleID: "ce-docs-changed", Kind: "changed", Target: "metadata", Impact: model.ImpactDocs,
			Message: "description changed"}, "/description")
	}

	oldByType := map[string]model.Message{}
	for _, m := range old.Spec.Messages {
		oldByType[m.Key] = m
	}
	newTypes := map[string]bool{}
	for _, nm := range new.Spec.Messages {
		newTypes[nm.Key] = true
		om, ok := oldByType[nm.Key]
		if !ok {
			d.addNew(model.Change{RuleID: "ce-message-added", Kind: "added", Target: "message", Type: nm.Key,
				Impact: model.ImpactAdditive, Message: fmt.Sprintf("%s (%s) was added", nm.Key, nm.Role)}, nm.Pointer)
			continue
		}
		if err := d.message(om, nm); err != nil {
			return nil, err
		}
	}
	for _, om := range old.Spec.Messages {
		if !newTypes[om.Key] {
			d.add(model.Change{RuleID: "ce-message-removed", Kind: "removed", Target: "message", Type: om.Key,
				Impact: model.ImpactBreaking, Message: fmt.Sprintf("%s (%s) was removed", om.Key, om.Role),
				File: old.Doc.Path, Pointer: om.Pointer, Line: old.Doc.Line(om.Pointer)})
		}
	}
	return d.changes, nil
}

type eventDiff struct {
	old, new *eventcatalog.Result
	override compat.Mode
	changes  []model.Change
}

func (d *eventDiff) add(c model.Change) {
	if c.Impact == model.ImpactBreaking {
		c.ID = changeID(c)
	}
	d.changes = append(d.changes, c)
}

// addNew records a change located at ptr in the new spec.
func (d *eventDiff) addNew(c model.Change, ptr string) {
	c.File, c.Pointer, c.Line = d.new.Doc.Path, ptr, d.new.Doc.Line(ptr)
	d.add(c)
}

// changeID is stable for identical input, so an ack can be put in CI
// config for a run.
func changeID(c model.Change) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{c.RuleID, c.Type, c.Field, c.Message}, "\x00")))
	return "BRK-CE-" + hex.EncodeToString(sum[:3])
}

func (d *eventDiff) message(om, nm model.Message) error {
	change := func(rule, target, field string, impact model.Impact, ptr, format string, args ...any) {
		d.addNew(model.Change{RuleID: rule, Kind: "changed", Target: target, Type: nm.Key, Field: field,
			Impact: impact, Message: nm.Key + ": " + fmt.Sprintf(format, args...)}, ptr)
	}

	if om.Role != nm.Role {
		change("ce-role-changed", "message", "", model.ImpactBreaking, nm.Pointer+"/role",
			"role changed from %s to %s", om.Role, nm.Role)
	}
	if err := d.payload(om, nm); err != nil {
		return err
	}
	d.bindings(om, nm)

	if om.CE.DataContentType != nm.CE.DataContentType {
		change("ce-datacontenttype-changed", "message", "", model.ImpactBreaking, nm.Pointer,
			"datacontenttype changed from %s to %s", om.CE.DataContentType, nm.CE.DataContentType)
	}
	d.extensions(om, nm)
	if om.CE.Source != nm.CE.Source {
		change("ce-source-changed", "message", "", model.ImpactWarn, nm.Pointer,
			"source pattern changed from %q to %q", om.CE.Source, nm.CE.Source)
	}
	if om.CE.Subject != nm.CE.Subject {
		change("ce-subject-changed", "message", "", model.ImpactWarn, nm.Pointer,
			"subject pattern changed from %q to %q", om.CE.Subject, nm.CE.Subject)
	}
	if om.CE.DataSchemaURI != nm.CE.DataSchemaURI {
		change("ce-dataschemauri-changed", "message", "", model.ImpactWarn, nm.Pointer,
			"dataschemauri changed from %q to %q", om.CE.DataSchemaURI, nm.CE.DataSchemaURI)
	}
	if om.Deprecated != nm.Deprecated {
		verb := "is now deprecated"
		if !nm.Deprecated {
			verb = "is no longer deprecated"
		}
		change("ce-message-deprecated", "message", "", model.ImpactAdditive, nm.Pointer, "%s", verb)
	}

	var docs []string
	if om.Summary != nm.Summary {
		docs = append(docs, "summary")
	}
	if om.Description != nm.Description {
		docs = append(docs, "description")
	}
	if !reflect.DeepEqual(om.Examples, nm.Examples) {
		docs = append(docs, "examples")
	}
	if len(docs) > 0 {
		change("ce-docs-changed", "message", "", model.ImpactDocs, nm.Pointer, "%s changed", strings.Join(docs, ", "))
	}
	return nil
}

// payload compares payload schemas; a message whose schema didn't resolve on
// either side has already been reported by the parser.
func (d *eventDiff) payload(om, nm model.Message) error {
	if om.Payload == "" || nm.Payload == "" {
		return nil
	}
	mode := d.override
	if mode == "" {
		mode = compat.DefaultMode(nm.Role)
	}
	oldS, newS := compat.FromSpec(d.old.Spec, om.Payload), compat.FromSpec(d.new.Spec, nm.Payload)
	issues, err := compat.Check(oldS, newS, mode)
	if err != nil {
		return err
	}
	ptr := nm.Pointer + "/dataschema"
	for _, is := range issues {
		field := is.Path
		if field == "" {
			field = "(root)"
		}
		d.addNew(model.Change{RuleID: is.Rule, Kind: "changed", Target: "schema-field", Type: nm.Key, Field: is.Path,
			Impact:  model.ImpactBreaking,
			Message: fmt.Sprintf("%s payload %s: %s (breaks %s)", nm.Key, field, is.Message, guarantee(is.Direction))}, ptr)
	}
	if len(issues) > 0 {
		return nil
	}
	same := func(annotations bool) (bool, error) {
		a, err := compat.Fingerprint(oldS, annotations)
		if err != nil {
			return false, err
		}
		b, err := compat.Fingerprint(newS, annotations)
		return a == b, err
	}
	sameConstraints, err := same(false)
	if err != nil {
		return err
	}
	if !sameConstraints {
		d.addNew(model.Change{RuleID: "ce-payload-changed", Kind: "changed", Target: "schema-field", Type: nm.Key,
			Impact: model.ImpactAdditive, Message: nm.Key + ": payload schema changed compatibly (" + string(mode) + ")"}, ptr)
		return nil
	}
	sameDocs, err := same(true)
	if err != nil {
		return err
	}
	if !sameDocs {
		d.addNew(model.Change{RuleID: "ce-docs-changed", Kind: "changed", Target: "schema-field", Type: nm.Key,
			Impact: model.ImpactDocs, Message: nm.Key + ": payload schema documentation changed"}, ptr)
	}
	return nil
}

func guarantee(m compat.Mode) string {
	switch m {
	case compat.Forward:
		return "FORWARD: consumers on the old schema can't read new events"
	case compat.Backward:
		return "BACKWARD: senders on the old schema would be rejected"
	}
	return "FORWARD and BACKWARD"
}

func bindingKey(b model.Binding) string { return b.Protocol + " " + b.Address }

func (d *eventDiff) bindings(om, nm model.Message) {
	oldB := map[string]model.Binding{}
	for _, b := range om.Bindings {
		oldB[bindingKey(b)] = b
	}
	newB := map[string]model.Binding{}
	for _, b := range nm.Bindings {
		newB[bindingKey(b)] = b
	}
	for _, b := range nm.Bindings {
		ob, ok := oldB[bindingKey(b)]
		if !ok {
			d.addNew(model.Change{RuleID: "ce-binding-added", Kind: "added", Target: "binding", Type: nm.Key, Field: b.Address,
				Impact: model.ImpactAdditive, Message: fmt.Sprintf("%s: now also carried on %s %s", nm.Key, b.Protocol, b.Address)}, b.Pointer)
			continue
		}
		if changed := changedProps(ob.Props, b.Props); len(changed) > 0 {
			d.addNew(model.Change{RuleID: "ce-binding-changed", Kind: "changed", Target: "binding", Type: nm.Key, Field: b.Address,
				Impact:  model.ImpactBreaking,
				Message: fmt.Sprintf("%s: %s %s changed %s", nm.Key, b.Protocol, b.Address, strings.Join(changed, ", "))}, b.Pointer)
		}
	}
	for _, b := range om.Bindings {
		if _, ok := newB[bindingKey(b)]; !ok {
			d.addNew(model.Change{RuleID: "ce-binding-removed", Kind: "removed", Target: "binding", Type: nm.Key, Field: b.Address,
				Impact: model.ImpactBreaking, Message: fmt.Sprintf("%s: no longer carried on %s %s", nm.Key, b.Protocol, b.Address)}, nm.Pointer)
		}
	}
}

// changedProps describes the binding properties that differ.
func changedProps(old, new map[string]any) []string {
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range new {
		keys[k] = true
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		ov, nv := old[k], new[k]
		if !reflect.DeepEqual(ov, nv) {
			out = append(out, fmt.Sprintf("%s from %s to %s", k, showProp(ov), showProp(nv)))
		}
	}
	return out
}

func showProp(v any) string {
	if v == nil {
		return "none"
	}
	if m, ok := v.(map[string]any); ok {
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(m)) {
			parts = append(parts, fmt.Sprintf("%s: %v", k, m[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func (d *eventDiff) extensions(om, nm model.Message) {
	names := map[string]bool{}
	for k := range om.CE.Extensions {
		names[k] = true
	}
	for k := range nm.CE.Extensions {
		names[k] = true
	}
	// Which side is hurt by a change in requirements depends on who writes:
	// for produces the owner writes, for receives other teams do.
	requiredAdded, requiredRemoved := model.ImpactAdditive, model.ImpactBreaking
	if nm.Role == model.RoleReceives {
		requiredAdded, requiredRemoved = model.ImpactBreaking, model.ImpactAdditive
	}
	ptr := nm.Pointer + "/extensions"
	for _, name := range slices.Sorted(maps.Keys(names)) {
		oe, inOld := om.CE.Extensions[name]
		ne, inNew := nm.CE.Extensions[name]
		ext := func(rule string, impact model.Impact, format string, args ...any) {
			d.addNew(model.Change{RuleID: rule, Kind: "changed", Target: "message", Type: nm.Key, Field: name,
				Impact: impact, Message: nm.Key + ": " + fmt.Sprintf(format, args...)}, ptr)
		}
		switch {
		case !inOld && ne.Required:
			ext("ce-extension-required-added", requiredAdded, "required extension %s was added", name)
		case !inOld:
			ext("ce-extension-changed", model.ImpactAdditive, "optional extension %s was added", name)
		case !inNew && oe.Required:
			ext("ce-extension-required-removed", requiredRemoved, "required extension %s was removed", name)
		case !inNew:
			ext("ce-extension-changed", model.ImpactAdditive, "optional extension %s was removed", name)
		default:
			if !oe.Required && ne.Required {
				ext("ce-extension-required-added", requiredAdded, "extension %s is now required", name)
			}
			if oe.Required && !ne.Required {
				ext("ce-extension-required-removed", requiredRemoved, "extension %s is no longer required", name)
			}
			if oe.Type != ne.Type {
				ext("ce-extension-type-changed", model.ImpactBreaking, "extension %s type changed from %s to %s",
					name, orNone(oe.Type), orNone(ne.Type))
			}
			if oe.Description != ne.Description {
				ext("ce-docs-changed", model.ImpactDocs, "extension %s description changed", name)
			}
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// SameContent reports whether two specs consist of the same files with the
// same bytes. It stands in for the bundle content hash.
func SameContent(oldRoot string, oldFiles []string, newRoot string, newFiles []string) (bool, error) {
	if !slices.Equal(sorted(oldFiles), sorted(newFiles)) {
		return false, nil
	}
	for _, f := range oldFiles {
		a, err := os.ReadFile(filepath.Join(oldRoot, filepath.FromSlash(f)))
		if err != nil {
			return false, err
		}
		b, err := os.ReadFile(filepath.Join(newRoot, filepath.FromSlash(f)))
		if err != nil {
			return false, err
		}
		if !bytes.Equal(a, b) {
			return false, nil
		}
	}
	return true, nil
}

func sorted(ss []string) []string {
	out := slices.Clone(ss)
	sort.Strings(out)
	return out
}
