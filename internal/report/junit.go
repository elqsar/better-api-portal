package report

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"

	"better-api-portal/internal/check"
	"better-api-portal/internal/model"
)

type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	SystemOut *junitText    `xml:"system-out,omitempty"`
}

// junitText is element text kept verbatim, newlines included.
type junitText struct {
	Text string `xml:",cdata"`
}

func cdata(s string) *junitText {
	if s == "" {
		return nil
	}
	return &junitText{s}
}

type junitFailure struct {
	Type    string `xml:"type,attr"`
	Message string `xml:"message,attr"`
	Body    string `xml:",cdata"`
}

// JUnit writes the report as JUnit XML: a suite per API, with the
// descriptor's own findings in a suite named after it, and a test case per
// finding. Failures are what fails the run: errors, and warnings when
// strict. Other findings pass, with their text in system-out. Each API also
// gets a passing case with its version, score and changes.
func JUnit(w io.Writer, r *check.Report, strict bool) error {
	// Suites in descriptor order: the descriptor's first, then parsed APIs,
	// then APIs whose spec couldn't be parsed.
	var order []string
	suites := map[string]*junitSuite{}
	suite := func(api string) *junitSuite {
		if s, ok := suites[api]; ok {
			return s
		}
		name := api
		if api == "" {
			name = r.Descriptor
		}
		s := &junitSuite{Name: name}
		suites[api] = s
		order = append(order, api)
		return s
	}
	for _, f := range r.Findings {
		if f.API == "" {
			suite("")
			break
		}
	}
	for _, a := range r.APIs {
		suite(a.ID)
	}

	for _, f := range r.Findings {
		s := suite(f.API)
		c := junitCase{Name: caseName(f), Classname: s.Name}
		if f.Severity == model.SeverityError || (strict && f.Severity == model.SeverityWarn) {
			c.Failure = &junitFailure{Type: string(f.Severity), Message: f.Message, Body: findingLine(f)}
			s.Failures++
		} else {
			c.SystemOut = cdata(findingLine(f))
		}
		s.Cases = append(s.Cases, c)
	}
	for _, a := range r.APIs {
		var out bytes.Buffer
		if a.BaselineVersion != "" {
			fmt.Fprintf(&out, "baseline %s, %d change(s)\n", a.BaselineVersion, len(a.Changes))
		}
		if err := Changes(&out, a.Changes, "  "); err != nil {
			return err
		}
		s := suite(a.ID)
		s.Cases = append(s.Cases, junitCase{Name: fmt.Sprintf("version %s score %d", a.Version, a.Score),
			Classname: a.ID, SystemOut: cdata(out.String())})
	}

	all := junitSuites{Name: "portal check"}
	for _, api := range order {
		s := suites[api]
		s.Tests = len(s.Cases)
		all.Tests += s.Tests
		all.Failures += s.Failures
		all.Suites = append(all.Suites, *s)
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(all); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// caseName is "rule [change id] [file[:line]]".
func caseName(f model.Finding) string {
	name := f.RuleID
	if f.ID != "" {
		name += " " + f.ID
	}
	if f.File != "" {
		name += " " + f.File
		if f.Line > 0 {
			name += fmt.Sprintf(":%d", f.Line)
		}
	}
	return name
}
