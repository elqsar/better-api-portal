// Package report renders check and diff results for CI logs and machines.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/elqsar/better-api-portal/internal/check"
	"github.com/elqsar/better-api-portal/internal/model"
)

// Text writes one line per finding, `file:line: severity [rule id] pointer: message`,
// then each API's version, score and changes, then a summary line.
func Text(w io.Writer, r *check.Report) error {
	for _, f := range r.Findings {
		if err := finding(w, f); err != nil {
			return err
		}
	}
	for _, a := range r.APIs {
		head := fmt.Sprintf("%s %s", a.ID, a.Version)
		if a.BaselineVersion != "" {
			head += fmt.Sprintf(" (baseline %s, %d change(s))", a.BaselineVersion, len(a.Changes))
		}
		if _, err := fmt.Fprintf(w, "%s: score %d\n", head, a.Score); err != nil {
			return err
		}
		if err := Changes(w, a.Changes, "  "); err != nil {
			return err
		}
	}
	c := model.Counts(r.Findings)
	_, err := fmt.Fprintf(w, "%d error(s), %d warning(s), %d info\n",
		c[model.SeverityError], c[model.SeverityWarn], c[model.SeverityInfo])
	return err
}

func finding(w io.Writer, f model.Finding) error {
	_, err := fmt.Fprintln(w, findingLine(f))
	return err
}

// findingLine is a finding as one line of text, without a newline.
func findingLine(f model.Finding) string {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", loc, f.Line)
	}
	if loc != "" {
		loc += ": "
	}
	rule := f.RuleID
	if f.ID != "" {
		rule += " " + f.ID
	}
	ptr := ""
	if f.Pointer != "" {
		ptr = f.Pointer + ": "
	}
	return fmt.Sprintf("%s%s [%s] %s%s", loc, f.Severity, rule, ptr, f.Message)
}

// Changes writes one line per change: impact, id (for breaking changes),
// rule and message.
func Changes(w io.Writer, changes []model.Change, indent string) error {
	for _, c := range changes {
		id := ""
		if c.ID != "" {
			id = c.ID + " "
		}
		if _, err := fmt.Fprintf(w, "%s%-8s  %s%s: %s\n", indent, c.Impact, id, c.RuleID, c.Message); err != nil {
			return err
		}
	}
	return nil
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
	return encode(w, out)
}

// ChangesJSON writes {"changes": [...]}.
func ChangesJSON(w io.Writer, changes []model.Change) error {
	if changes == nil {
		changes = []model.Change{}
	}
	return encode(w, struct {
		Changes []model.Change `json:"changes"`
	}{changes})
}

func encode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
