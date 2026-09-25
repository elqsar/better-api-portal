// Package report renders check results for CI logs and machines.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"better-api-portal/internal/check"
	"better-api-portal/internal/model"
)

// Text writes one line per finding, `file:line: severity [rule] pointer: message`,
// then each API's score, then a summary line.
func Text(w io.Writer, r *check.Report) error {
	for _, f := range r.Findings {
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
	for _, a := range r.APIs {
		if _, err := fmt.Fprintf(w, "%s: score %d\n", a.ID, a.Score); err != nil {
			return err
		}
	}
	c := model.Counts(r.Findings)
	_, err := fmt.Fprintf(w, "%d error(s), %d warning(s), %d info\n",
		c[model.SeverityError], c[model.SeverityWarn], c[model.SeverityInfo])
	return err
}

// JSON writes {"findings": [...], "apis": [...]}; the lists are never null.
func JSON(w io.Writer, r *check.Report) error {
	out := struct {
		Findings []model.Finding   `json:"findings"`
		APIs     []check.APIResult `json:"apis"`
	}{r.Findings, r.APIs}
	if out.Findings == nil {
		out.Findings = []model.Finding{}
	}
	if out.APIs == nil {
		out.APIs = []check.APIResult{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
