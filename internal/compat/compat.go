// Package compat decides whether a change to a JSON Schema breaks the
// producers or consumers of the events it describes
// (docs/spec/04-governance.md §2, "Event schema compatibility").
//
// Every mode reduces to one question, sub(A, B): is every instance valid
// under A also valid under B? FORWARD asks sub(new, old), BACKWARD asks
// sub(old, new). The check is structural and conservative: over a subset of
// keywords it reports anything it can't show to be compatible, and any change
// involving other keywords is BRK-UNVERIFIED.
package compat

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"better-api-portal/internal/model"
)

// Mode is a compatibility guarantee, in Kafka Schema Registry's vocabulary.
type Mode string

const (
	// Forward: every event valid under new is valid under old, so consumers
	// still on the old schema can read new events.
	Forward Mode = "FORWARD"
	// Backward: every event valid under old is valid under new, so the owner
	// still accepts events from senders on the old schema.
	Backward Mode = "BACKWARD"
	Full     Mode = "FULL"
	None     Mode = "NONE"
)

// DefaultMode protects whichever side doesn't control the upgrade (D6).
func DefaultMode(r model.Role) Mode {
	if r == model.RoleReceives {
		return Backward
	}
	return Forward
}

// Rule ids of issues.
const (
	RuleType                 = "compat-type"
	RuleEnum                 = "compat-enum"
	RuleRequired             = "compat-required"
	RuleAdditionalProperties = "compat-additional-properties"
	RuleConstraint           = "compat-constraint"
	RuleUnverified           = "BRK-UNVERIFIED"
)

// Issue is one reason a change breaks a guarantee.
type Issue struct {
	Rule string
	// Direction is the guarantee that breaks: FORWARD, BACKWARD, or FULL
	// when the same issue breaks both.
	Direction Mode
	// Path is the instance location: "/total/amount", "/lines/[]/sku" for
	// array items, "/*" for additional properties, "" for the root.
	Path    string
	Message string
}

// Check compares the old and new payload schemas under mode m. The error is
// reserved for a root schema that can't be found.
func Check(old, new Schema, m Mode) ([]Issue, error) {
	oldRoot, err := old.root()
	if err != nil {
		return nil, err
	}
	newRoot, err := new.root()
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if m == Forward || m == Full {
		c := &checker{a: new, b: old, dir: Forward, stack: map[[2]loc]bool{}}
		c.sub(newRoot, oldRoot, "")
		issues = append(issues, c.issues...)
	}
	if m == Backward || m == Full {
		c := &checker{a: old, b: new, dir: Backward, stack: map[[2]loc]bool{}}
		c.sub(oldRoot, newRoot, "")
		issues = append(issues, c.issues...)
	}
	if m == Full {
		issues = mergeDirections(issues)
	}
	sort.Slice(issues, func(i, j int) bool {
		x, y := issues[i], issues[j]
		if x.Direction != y.Direction {
			return x.Direction < y.Direction
		}
		if x.Path != y.Path {
			return x.Path < y.Path
		}
		if x.Rule != y.Rule {
			return x.Rule < y.Rule
		}
		return x.Message < y.Message
	})
	return issues, nil
}

// mergeDirections reports an issue found in both directions once, as FULL.
func mergeDirections(issues []Issue) []Issue {
	type key struct{ rule, path, msg string }
	count := map[key]int{}
	for _, is := range issues {
		count[key{is.Rule, is.Path, is.Message}]++
	}
	var out []Issue
	seen := map[key]bool{}
	for _, is := range issues {
		k := key{is.Rule, is.Path, is.Message}
		if count[k] > 1 {
			if seen[k] {
				continue
			}
			seen[k] = true
			is.Direction = Full
		}
		out = append(out, is)
	}
	return out
}

// checker evaluates sub(A, B) for one direction.
type checker struct {
	a, b   Schema // instances of a must be valid under b
	dir    Mode
	issues []Issue
	// stack holds the node pairs being compared. Meeting one again means a
	// recursive schema came back to where it started, and the pair is
	// assumed compatible (coinduction).
	stack map[[2]loc]bool
}

func (c *checker) report(rule, path, format string, args ...any) {
	c.issues = append(c.issues, Issue{Rule: rule, Direction: c.dir, Path: path, Message: fmt.Sprintf(format, args...)})
}

// oldNew orders values of the A and B sides as (old, new) for messages.
func (c *checker) oldNew(a, b any) (any, any) {
	if c.dir == Forward {
		return b, a
	}
	return a, b
}

// aIsNew reports whether side A is the new schema.
func (c *checker) aIsNew() bool { return c.dir == Forward }

const maxRefHops = 32

// deref follows $refs until it reaches a node without one. A $ref with
// constraint siblings can't be reasoned about, so it fails.
func (c *checker) deref(s Schema, n node) (node, error) {
	for range maxRefHops {
		m, ok := n.v.(map[string]any)
		if !ok {
			return n, nil
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return n, nil
		}
		for k := range m {
			if k != "$ref" && !annotationKeywords[k] {
				return n, fmt.Errorf("$ref %s has sibling keyword %s", ref, k)
			}
		}
		next, err := s.resolve(n.at, ref)
		if err != nil {
			return n, err
		}
		n = next
	}
	return n, fmt.Errorf("$ref chain longer than %d", maxRefHops)
}

func (c *checker) sub(an, bn node, path string) {
	an, errA := c.deref(c.a, an)
	bn, errB := c.deref(c.b, bn)
	if errA != nil || errB != nil {
		c.report(RuleUnverified, path, "can't verify: %v", firstErr(errA, errB))
		return
	}

	key := [2]loc{an.at, bn.at}
	if c.stack[key] {
		return
	}
	c.stack[key] = true
	defer delete(c.stack, key)

	a, aFalse := asSchema(an.v)
	b, bFalse := asSchema(bn.v)
	switch {
	case aFalse:
		return // A admits nothing, so B accepts all of it
	case bFalse:
		c.report(RuleType, path, "the %s schema accepts no value here", c.side(false))
		return
	}

	if kw := unionKeywords(unsupportedKeywords(a), unsupportedKeywords(b)); len(kw) > 0 {
		if !equal(constraints(a), constraints(b)) || containsRef(a) || containsRef(b) {
			c.report(RuleUnverified, path, "can't verify a change involving %s", strings.Join(kw, ", "))
		}
		return
	}

	ta, tb := types(a), types(b)
	c.checkType(ta, tb, path)
	c.checkEnum(a, b, path)

	if allows(ta, "object") && allows(tb, "object") {
		c.checkObject(an, bn, a, b, path)
	}
	if allows(ta, "array") && allows(tb, "array") {
		c.checkArray(an, bn, a, b, path)
	}
	if allows(ta, "string") && allows(tb, "string") {
		c.checkLowerBound(a, b, "minLength", path)
		c.checkUpperBound(a, b, "maxLength", path)
		c.checkExact(a, b, "pattern", path)
		c.checkExact(a, b, "format", path)
	}
	if numeric(ta) && numeric(tb) {
		c.checkLowerBound(a, b, "minimum", path)
		c.checkUpperBound(a, b, "maximum", path)
	}
}

// numeric reports whether a type set admits numbers of any kind.
func numeric(ts []string) bool { return allows(ts, "number") || allows(ts, "integer") }

// side names the old or new schema, from the A (true) or B (false) side.
func (c *checker) side(a bool) string {
	if a == c.aIsNew() {
		return "new"
	}
	return "old"
}

func (c *checker) checkType(ta, tb []string, path string) {
	if tb == nil {
		return
	}
	var extra []string
	for _, t := range allTypesOr(ta) {
		if !allows(tb, t) {
			extra = append(extra, t)
		}
	}
	// A schema with no type allows all of them; say so rather than listing.
	if len(extra) > 0 {
		o, n := c.oldNew(showTypes(ta), showTypes(tb))
		c.report(RuleType, path, "type changed from %s to %s", o, n)
	}
}

func allTypesOr(ts []string) []string {
	if ts == nil {
		return allTypes
	}
	return ts
}

func (c *checker) checkEnum(a, b map[string]any, path string) {
	eb := enumOf(b)
	if eb == nil {
		return
	}
	ea := enumOf(a)
	if ea == nil {
		if c.aIsNew() {
			c.report(RuleEnum, path, "enum %s was removed: any value is now allowed", show(eb))
		} else {
			c.report(RuleEnum, path, "enum %s was added: other values are no longer allowed", show(eb))
		}
		return
	}
	var extra []any
	for _, v := range ea {
		if !containsValue(eb, v) {
			extra = append(extra, v)
		}
	}
	if len(extra) == 0 {
		return
	}
	if c.aIsNew() {
		c.report(RuleEnum, path, "enum gained %s", show(extra))
	} else {
		c.report(RuleEnum, path, "enum lost %s", show(extra))
	}
}

func (c *checker) checkObject(an, bn node, a, b map[string]any, path string) {
	// B.required ⊆ A.required: anything B needs, every A-instance must have.
	reqA, reqB := stringSet(a["required"]), stringSet(b["required"])
	propsA, _ := a["properties"].(map[string]any)
	propsB, _ := b["properties"].(map[string]any)
	for _, r := range sortedKeys(reqB) {
		if reqA[r] {
			continue
		}
		_, inA := propsA[r]
		_, inB := propsB[r]
		switch {
		case c.aIsNew() && !inA:
			c.report(RuleRequired, path+"/"+escape(r), "required property %s was removed", r)
		case c.aIsNew():
			c.report(RuleRequired, path+"/"+escape(r), "%s is no longer required", r)
		case !inA && inB:
			c.report(RuleRequired, path+"/"+escape(r), "required property %s was added", r)
		default:
			c.report(RuleRequired, path+"/"+escape(r), "%s is now required", r)
		}
	}

	addA, addB := a["additionalProperties"], b["additionalProperties"]
	for _, p := range sortedKeys(propsA) {
		pp := path + "/" + escape(p)
		pa := node{propsA[p], an.at.child("properties", p)}
		if vb, ok := propsB[p]; ok {
			c.sub(pa, node{vb, bn.at.child("properties", p)}, pp)
			continue
		}
		// Only A declares p. B accepts it as an additional property, if at all.
		// This is the governance table's footnote: adding (FORWARD) or
		// removing (BACKWARD) a property breaks when the side that must accept
		// the instance has additionalProperties: false.
		switch {
		case isFalse(addB):
			if c.aIsNew() {
				c.report(RuleAdditionalProperties, pp, "property %s was added, but the old schema doesn't allow additional properties", p)
			} else {
				c.report(RuleAdditionalProperties, pp, "property %s was removed, and the new schema doesn't allow additional properties", p)
			}
		case addB != nil && !isTrue(addB):
			c.sub(pa, node{addB, bn.at.child("additionalProperties")}, pp)
		}
	}
	// Only B declares p: A-instances carry it only as an additional property.
	// An open A is fine by the table's convention; an A with an
	// additionalProperties schema must fit B's declaration.
	if addA != nil && !isTrue(addA) && !isFalse(addA) {
		for _, p := range sortedKeys(propsB) {
			if _, ok := propsA[p]; !ok {
				c.sub(node{addA, an.at.child("additionalProperties")}, node{propsB[p], bn.at.child("properties", p)}, path+"/"+escape(p))
			}
		}
	}

	// additionalProperties itself: if B restricts undeclared fields, A must
	// restrict them at least as much.
	switch {
	case addB == nil || isTrue(addB) || isFalse(addA):
	case isFalse(addB):
		c.reportClosed(path)
	case addA == nil || isTrue(addA):
		c.reportClosed(path)
	default:
		c.sub(node{addA, an.at.child("additionalProperties")}, node{addB, bn.at.child("additionalProperties")}, path+"/*")
	}
}

func (c *checker) reportClosed(path string) {
	if c.aIsNew() {
		c.report(RuleAdditionalProperties, path, "additionalProperties is no longer restricted")
	} else {
		c.report(RuleAdditionalProperties, path, "additionalProperties is now restricted")
	}
}

func (c *checker) checkArray(an, bn node, a, b map[string]any, path string) {
	ib, ok := b["items"]
	if !ok || isTrue(ib) {
		return
	}
	ia, ok := a["items"]
	if !ok || isTrue(ia) {
		if c.aIsNew() {
			c.report(RuleConstraint, path+"/[]", "the items schema was removed: any item is now allowed")
		} else {
			c.report(RuleConstraint, path+"/[]", "an items schema was added")
		}
		return
	}
	_, tupleA := ia.([]any)
	_, tupleB := ib.([]any)
	if tupleA || tupleB {
		if !equal(ia, ib) || containsRef(ia) || containsRef(ib) {
			c.report(RuleUnverified, path+"/[]", "can't verify a change involving tuple items")
		}
		return
	}
	c.sub(node{ia, an.at.child("items")}, node{ib, bn.at.child("items")}, path+"/[]")
}

// checkLowerBound: A's lower bound must be at least B's.
func (c *checker) checkLowerBound(a, b map[string]any, kw, path string) {
	c.checkBound(a, b, kw, path, func(fa, fb float64) bool { return fa >= fb })
}

// checkUpperBound: A's upper bound must be at most B's.
func (c *checker) checkUpperBound(a, b map[string]any, kw, path string) {
	c.checkBound(a, b, kw, path, func(fa, fb float64) bool { return fa <= fb })
}

func (c *checker) checkBound(a, b map[string]any, kw, path string, within func(fa, fb float64) bool) {
	vb, ok := b[kw]
	if !ok {
		return
	}
	fb, okB := toFloat(vb)
	va, hasA := a[kw]
	fa, okA := toFloat(va)
	if hasA && okA && okB && within(fa, fb) {
		return
	}
	var av any = "none"
	if hasA {
		av = va
	}
	o, n := c.oldNew(av, vb)
	c.report(RuleConstraint, path, "%s changed from %s to %s", kw, showBound(o), showBound(n))
}

// checkExact: keywords such as pattern can't be compared, so B may only
// keep A's value or drop the keyword.
func (c *checker) checkExact(a, b map[string]any, kw, path string) {
	vb, ok := b[kw]
	if !ok {
		return
	}
	va, hasA := a[kw]
	if hasA && equal(va, vb) {
		return
	}
	var av any = "none"
	if hasA {
		av = va
	}
	o, n := c.oldNew(av, vb)
	c.report(RuleConstraint, path, "%s changed from %s to %s", kw, showBound(o), showBound(n))
}

func showBound(v any) string {
	if s, ok := v.(string); ok && s == "none" {
		return s
	}
	return show(v)
}

// asSchema returns a schema as a map, and whether it is the false schema.
// true and {} both accept anything.
func asSchema(v any) (map[string]any, bool) {
	switch v := v.(type) {
	case bool:
		return map[string]any{}, !v
	case map[string]any:
		return v, false
	}
	return map[string]any{}, false
}

func isFalse(v any) bool { b, ok := v.(bool); return ok && !b }

func isTrue(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	m, ok := v.(map[string]any)
	return ok && len(constraints(m)) == 0
}

func stringSet(v any) map[string]bool {
	out := map[string]bool{}
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func unionKeywords(a, b []string) []string {
	out := slices.Clone(a)
	for _, k := range b {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
