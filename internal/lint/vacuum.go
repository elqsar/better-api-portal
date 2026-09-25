package lint

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/daveshanley/vacuum/motor"
	"github.com/daveshanley/vacuum/rulesets"

	"better-api-portal/internal/model"
	"better-api-portal/internal/yamldoc"
)

// recommended is vacuum's built-in OpenAPI ruleset, built once.
var recommended = sync.OnceValue(func() *rulesets.RuleSet {
	return rulesets.BuildDefaultRuleSets().GenerateOpenAPIRecommendedRuleSet()
})

// runVacuum lints the entry file with vacuum's recommended rules. Remote
// lookups are off; local $refs resolve from dir.
func runVacuum(t OpenAPITarget) []model.Finding {
	res := motor.ApplyRulesToRuleSet(&motor.RuleSetExecution{
		RuleSet:      recommended(),
		Spec:         t.Bytes,
		SpecFileName: t.File,
		Base:         t.Dir,
		SilenceLogs:  true,
	})
	var fs []model.Finding
	for _, r := range res.Results {
		f := model.Finding{
			RuleID:   r.RuleId,
			Severity: severity(r.RuleSeverity),
			Message:  r.Message,
			File:     t.File,
		}
		// Prefer our own pointer and line: vacuum's JSONPath converts to a
		// pointer for most rules, and its node lines are often unset.
		if ptr, ok := jsonPathToPointer(r.Path); ok && t.Doc.Has(ptr) {
			f.Pointer, f.Line = ptr, t.Doc.Line(ptr)
		} else if r.StartNode != nil {
			f.Line = r.StartNode.Line
		}
		fs = append(fs, f)
	}
	for _, err := range res.Errors {
		fs = append(fs, model.Finding{RuleID: "vacuum", Severity: model.SeverityError, File: t.File,
			Message: "the linter failed: " + err.Error()})
	}
	// vacuum runs rules concurrently; sort for stable output.
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		return a.Message < b.Message
	})
	return fs
}

func severity(s string) model.Severity {
	switch s {
	case "error":
		return model.SeverityError
	case "warn":
		return model.SeverityWarn
	}
	return model.SeverityInfo // info and hint
}

// jsonPathToPointer converts the simple JSONPaths vacuum reports, such as
// $.paths['/orders/{id}'].get.responses['200'], to JSON pointers.
func jsonPathToPointer(p string) (string, bool) {
	if !strings.HasPrefix(p, "$") {
		return "", false
	}
	var tokens []string
	s := p[1:]
	for len(s) > 0 {
		switch s[0] {
		case '.':
			s = s[1:]
			end := strings.IndexAny(s, ".[")
			if end < 0 {
				end = len(s)
			}
			if end == 0 {
				return "", false
			}
			tokens = append(tokens, s[:end])
			s = s[end:]
		case '[':
			if strings.HasPrefix(s, "['") {
				end := strings.Index(s, "']")
				if end < 0 {
					return "", false
				}
				tokens = append(tokens, s[2:end])
				s = s[end+2:]
				continue
			}
			end := strings.IndexByte(s, ']')
			if end < 0 {
				return "", false
			}
			if _, err := strconv.Atoi(s[1:end]); err != nil {
				return "", false
			}
			tokens = append(tokens, s[1:end])
			s = s[end+1:]
		default:
			return "", false
		}
	}
	return yamldoc.Pointer(tokens...), true
}
