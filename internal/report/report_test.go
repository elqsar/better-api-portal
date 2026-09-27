package report

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/check"
	"github.com/elqsar/better-api-portal/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

// sample covers what the renderers treat differently: a descriptor-level
// finding without a file, severities, an acknowledged breaking change, a
// finding in a baseline outside the checkout, an API with changes and a
// clean one.
func sample() *check.Report {
	const spec = "svc/api/openapi.yaml"
	return &check.Report{
		Descriptor: "svc/portal.yaml",
		Findings: []model.Finding{
			{RuleID: "descriptor-owner", Severity: model.SeverityError, Message: "owner team-x is not <known> & \"listed\""},
			{API: "orders-http", RuleID: "request-property-enum-value-removed", ID: "BRK-OA-29783f", Severity: model.SeverityError,
				Message: "breaking change needs version 3.0.0 or later: removed the enum value", File: spec, Pointer: "/paths/~1orders/post", Line: 33},
			{API: "orders-http", RuleID: "api-path-removed-without-deprecation", ID: "BRK-OA-b7eb78", Severity: model.SeverityInfo,
				Message: "api path removed (acknowledged: unused)", File: "/elsewhere/base/api/openapi.yaml", Pointer: "/paths/~1old/get", Line: 12},
			{API: "orders-http", RuleID: "sec-integer-bounds", Severity: model.SeverityWarn,
				Message: "integer has no minimum or maximum", File: spec, Pointer: "/components/schemas/Money", Line: 75},
			{API: "orders-events", RuleID: "ce-description", Severity: model.SeverityWarn,
				Message: "no description", File: "svc/api/events.yaml", Pointer: "/messages/1", Line: 32},
		},
		APIs: []check.APIResult{
			{ID: "orders-http", Score: 84, Version: "2.4.0", BaselineVersion: "2.3.0", Changes: []model.Change{
				{ID: "BRK-OA-29783f", RuleID: "request-property-enum-value-removed", Impact: model.ImpactBreaking, Message: "removed the enum value"},
				{RuleID: "endpoint-added", Impact: model.ImpactAdditive, Message: "endpoint added"},
			}},
			{ID: "orders-events", Score: 98, Version: "1.4.0"},
		},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file; rerun with -update and review the diff\n%s", name, got)
	}
}

func root(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestText(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "text.golden", buf.Bytes())
}

func TestSARIF(t *testing.T) {
	defer func(v func() string) { toolVersion = v }(toolVersion)
	toolVersion = func() string { return "v1.2.3" }

	var buf bytes.Buffer
	if err := SARIF(&buf, sample(), root(t)); err != nil {
		t.Fatal(err)
	}
	golden(t, "sarif.golden.json", buf.Bytes())

	var log sarifLog
	if err := json.Unmarshal(buf.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	run := log.Runs[0]
	if len(run.Results) != len(sample().Findings) {
		t.Fatalf("%d results for %d findings", len(run.Results), len(sample().Findings))
	}
	for _, r := range run.Results {
		if run.Tool.Driver.Rules[r.RuleIndex].ID != r.RuleID {
			t.Errorf("ruleIndex %d of %s points at %s", r.RuleIndex, r.RuleID, run.Tool.Driver.Rules[r.RuleIndex].ID)
		}
		uri := r.Locations[0].PhysicalLocation.ArtifactLocation.URI
		if uri == "" || filepath.IsAbs(uri) || strings.HasPrefix(uri, "/") || strings.Contains(uri, "..") {
			t.Errorf("%s: location %q is not inside the checkout", r.RuleID, uri)
		}
	}
	// The baseline finding falls back to the descriptor and says where it is.
	b := run.Results[2]
	if b.Locations[0].PhysicalLocation.ArtifactLocation.URI != "svc/portal.yaml" ||
		!strings.HasSuffix(b.Message.Text, "(at /elsewhere/base/api/openapi.yaml:12, outside the checkout)") || b.Level != "note" {
		t.Errorf("baseline result = %+v", b)
	}
}

// With the descriptor outside the checkout too, there is nothing inside to
// point at: every location is its own absolute file URI.
func TestSARIFOutsideRoot(t *testing.T) {
	r := sample()
	var buf bytes.Buffer
	if err := SARIF(&buf, r, "/somewhere/else"); err != nil {
		t.Fatal(err)
	}
	var log sarifLog
	if err := json.Unmarshal(buf.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	for i, res := range log.Runs[0].Results {
		a := res.Locations[0].PhysicalLocation.ArtifactLocation
		if !strings.HasPrefix(a.URI, "file:///") || a.URIBaseID != "" || strings.Contains(res.Message.Text, "outside the checkout") {
			t.Errorf("result %d: %+v %q", i, a, res.Message.Text)
		}
	}
	if u := log.Runs[0].Results[2].Locations[0].PhysicalLocation.ArtifactLocation.URI; u != "file:///elsewhere/base/api/openapi.yaml" {
		t.Errorf("baseline uri = %q", u)
	}
}

func TestSARIFFingerprintIgnoresLines(t *testing.T) {
	f := sample().Findings[1]
	g := f
	g.Line += 10
	if fingerprint(f) != fingerprint(g) {
		t.Error("moving a finding changed its fingerprint")
	}
	g.Message += "!"
	if fingerprint(f) == fingerprint(g) {
		t.Error("a different finding has the same fingerprint")
	}
}

func TestJUnit(t *testing.T) {
	for _, tt := range []struct {
		strict   bool
		file     string
		failures int
	}{
		{false, "junit.golden.xml", 2}, // the two errors
		{true, "junit-strict.golden.xml", 4},
	} {
		var buf bytes.Buffer
		if err := JUnit(&buf, sample(), tt.strict); err != nil {
			t.Fatal(err)
		}
		golden(t, tt.file, buf.Bytes())

		var got junitSuites
		if err := xml.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		// 5 findings plus a summary case per API.
		if got.Failures != tt.failures || got.Tests != 7 {
			t.Errorf("strict=%v: tests=%d failures=%d, want 7 and %d", tt.strict, got.Tests, got.Failures, tt.failures)
		}
		var names []string
		for _, s := range got.Suites {
			names = append(names, s.Name)
		}
		if strings.Join(names, ",") != "svc/portal.yaml,orders-http,orders-events" {
			t.Errorf("suites = %v", names)
		}
	}
}

func TestJUnitKeepsAwkwardText(t *testing.T) {
	msg := "pattern ]]> and <tags> & \"quotes\"\nsecond line"
	r := &check.Report{Descriptor: "portal.yaml", Findings: []model.Finding{
		{RuleID: "a", Severity: model.SeverityError, Message: msg},
		{RuleID: "b", Severity: model.SeverityInfo, Message: msg},
	}}
	var buf bytes.Buffer
	if err := JUnit(&buf, r, false); err != nil {
		t.Fatal(err)
	}
	var got junitSuites
	if err := xml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, buf.Bytes())
	}
	cases := got.Suites[0].Cases
	if cases[0].Failure.Message != msg || !strings.HasSuffix(cases[0].Failure.Body, msg) ||
		!strings.HasSuffix(cases[1].SystemOut.Text, msg) {
		t.Errorf("text didn't survive:\n%s", buf.Bytes())
	}
}
