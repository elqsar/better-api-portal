// Package report renders findings for CI logs and machines.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"better-api-portal/internal/model"
)

// Text writes one line per finding, `file:line: severity [rule] pointer: message`,
// followed by a summary line.
func Text(w io.Writer, findings []model.Finding) error {
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Line)
		}
		ptr := ""
		if f.Pointer != "" {
			ptr = f.Pointer + ": "
		}
		if _, err := fmt.Fprintf(w, "%s: %s [%s] %s%s\n", loc, f.Severity, f.RuleID, ptr, f.Message); err != nil {
			return err
		}
	}
	c := model.Counts(findings)
	_, err := fmt.Fprintf(w, "%d error(s), %d warning(s), %d info\n",
		c[model.SeverityError], c[model.SeverityWarn], c[model.SeverityInfo])
	return err
}

// JSON writes {"findings": [...]}; the list is never null.
func JSON(w io.Writer, findings []model.Finding) error {
	if findings == nil {
		findings = []model.Finding{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Findings []model.Finding `json:"findings"`
	}{findings})
}
