// Package lint runs rulesets over a parsed spec and scores the result
// (docs/spec/04-governance.md §1).
package lint

import (
	"github.com/santhosh-tekuri/jsonschema/v6"

	"better-api-portal/internal/model"
)

// Config holds the org settings rules depend on.
type Config struct {
	// EventTypePrefix is required of every event type; empty disables
	// ce-type-prefix.
	EventTypePrefix string
}

// Target is what a ruleset runs over.
type Target struct {
	Spec *model.Spec
	// Payloads holds the compiled payload schema of each message, by type.
	// A message whose schema didn't resolve has none.
	Payloads map[string]*jsonschema.Schema
	File     string               // the entry file, named in findings
	Line     func(ptr string) int // resolves a pointer in File to its line
}

// rule is one check. Its severity lives here rather than in the check, so
// rulesets can override it later.
type rule struct {
	id       string
	severity model.Severity
	check    func(t Target, cfg Config, report func(ptr, msg string))
}

func run(rules []rule, t Target, cfg Config) []model.Finding {
	var findings []model.Finding
	for _, r := range rules {
		r.check(t, cfg, func(ptr, msg string) {
			findings = append(findings, model.Finding{
				RuleID:   r.id,
				Severity: r.severity,
				Message:  msg,
				File:     t.File,
				Pointer:  ptr,
				Line:     t.Line(ptr),
			})
		})
	}
	return findings
}

// Score is 100 minus 10 per error and 2 per warning, floored at 0. It is for
// visibility only: gating uses severities.
func Score(findings []model.Finding) int {
	c := model.Counts(findings)
	return max(0, 100-10*c[model.SeverityError]-2*c[model.SeverityWarn])
}
