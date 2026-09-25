// Package model holds the normalised entities shared by the pipeline packages.
package model

// Severity of a finding. Gating uses severities, never the score.
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
	SeverityInfo  Severity = "info"
)

// Finding is one result of a check: a schema violation, a lint rule, a
// breaking change, and so on.
type Finding struct {
	API      string   `json:"api,omitempty"` // id of the API the finding is about, if any
	RuleID   string   `json:"rule_id"`
	ID       string   `json:"id,omitempty"` // the change id to acknowledge, for breaking-change findings
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"`
	Pointer  string   `json:"pointer,omitempty"`
	Line     int      `json:"line,omitempty"`
}

// Counts returns the number of findings per severity.
func Counts(fs []Finding) map[Severity]int {
	c := make(map[Severity]int, 3)
	for _, f := range fs {
		c[f.Severity]++
	}
	return c
}
