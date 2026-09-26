package check

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/descriptor"
)

// upload reads the descriptor at descPath and bundles its APIs, as
// portal push sends them.
func upload(t *testing.T, descPath string) ([]byte, map[string]*bundle.Bundle) {
	t.Helper()
	desc, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := descriptor.Load(descPath)
	if err != nil {
		t.Fatal(err)
	}
	bundles := map[string]*bundle.Bundle{}
	for i, a := range d.APIs {
		c, problems, err := bundle.Load(d.Dir, d.SpecPath(i))
		if err != nil || len(problems) > 0 {
			t.Fatalf("closure: %v %+v", err, problems)
		}
		if bundles[a.ID], err = bundle.Read(c.Root, c.Files()); err != nil {
			t.Fatal(err)
		}
	}
	return desc, bundles
}

// noTempPaths fails if the report mentions a temporary directory.
func noTempPaths(t *testing.T, r *Report) {
	t.Helper()
	tmp := os.TempDir()
	for _, f := range r.Findings {
		if strings.Contains(f.File, tmp) || strings.Contains(f.Message, tmp) || strings.Contains(f.File, "portal-") {
			t.Errorf("temporary path in finding: %+v", f)
		}
	}
	for _, a := range r.APIs {
		for _, c := range a.Changes {
			if strings.Contains(c.File, tmp) || strings.Contains(c.Message, tmp) || strings.Contains(c.File, "portal-") {
				t.Errorf("temporary path in change: %+v", c)
			}
		}
	}
}

func TestRunBundlesMatchesRun(t *testing.T) {
	desc, bundles := upload(t, example+"/portal.yaml")
	got, err := RunBundles(desc, bundles, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := Run(example+"/portal.yaml", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.APIs, want.APIs) {
		t.Errorf("apis = %+v, want %+v", got.APIs, want.APIs)
	}
	if len(got.Findings) != len(want.Findings) {
		t.Fatalf("%d findings, want %d", len(got.Findings), len(want.Findings))
	}
	for i, f := range got.Findings {
		w := want.Findings[i]
		rel, err := filepath.Rel(example, w.File)
		if err != nil {
			t.Fatal(err)
		}
		w.File = filepath.ToSlash(rel)
		if f != w {
			t.Errorf("finding %d = %+v, want %+v", i, f, w)
		}
	}
	if got.Descriptor != DescriptorName {
		t.Errorf("descriptor = %q", got.Descriptor)
	}
	noTempPaths(t, got)
}

func TestRunBundlesBaseline(t *testing.T) {
	base, head := baselinePair(t, map[string][2]string{"api/openapi.yaml": dropOutOfStock})
	bump(t, head, "api/openapi.yaml", "version: 2.3.0", "version: 2.4.0")
	_, baseBundles := upload(t, base)
	desc, bundles := upload(t, head)

	r, err := RunBundles(desc, bundles, Options{BaselineBundles: map[string]Baseline{
		"orders-http":   {Bundle: baseBundles["orders-http"], Lifecycle: "production"},
		"orders-events": {Bundle: baseBundles["orders-events"], Lifecycle: "deprecated"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if errs := errorsOf(r); len(errs) != 1 || !strings.HasPrefix(errs[0], "request-property-enum-value-removed BRK-OA-") {
		t.Errorf("errors = %v", errs)
	}
	// Unlike a bundle file, an in-memory baseline carries its lifecycle.
	var reversal bool
	for _, f := range r.Findings {
		reversal = reversal || (f.RuleID == "lifecycle-reversal" && f.API == "orders-events")
	}
	if !reversal {
		t.Errorf("no lifecycle-reversal for orders-events: %+v", r.Findings)
	}
	if a := httpAPI(t, r); a.BaselineVersion != "2.3.0" || len(a.Changes) != 1 || a.Changes[0].File != "api/openapi.yaml" {
		t.Errorf("api = %+v", a)
	}
	noTempPaths(t, r)
}

func TestRunBundlesBaselineConflict(t *testing.T) {
	base, head := baselinePair(t, nil)
	_, baseBundles := upload(t, base)
	desc, bundles := upload(t, head)
	_, err := RunBundles(desc, bundles, Options{
		Baselines:       []string{packBase(t, base, "orders-http")},
		BaselineBundles: map[string]Baseline{"orders-http": {Bundle: baseBundles["orders-http"]}},
	})
	if err == nil || !strings.Contains(err.Error(), "more than one baseline") {
		t.Errorf("err = %v", err)
	}
}

func TestRunBundlesRejectsMismatch(t *testing.T) {
	for name, edit := range map[string]func(map[string]*bundle.Bundle){
		"missing bundle": func(bs map[string]*bundle.Bundle) { delete(bs, "orders-events") },
		"unknown id":     func(bs map[string]*bundle.Bundle) { bs["nope"] = bs["orders-http"] },
		"wrong entry": func(bs map[string]*bundle.Bundle) {
			bs["orders-http"] = &bundle.Bundle{Entry: "api/events.yaml", Files: bs["orders-events"].Files}
		},
		"conflicting shared file": func(bs map[string]*bundle.Bundle) {
			files := map[string][]byte{}
			for p, data := range bs["orders-events"].Files {
				files[p] = data
			}
			files["api/openapi.yaml"] = []byte("openapi: 3.1.0\n")
			bs["orders-events"] = &bundle.Bundle{Entry: bs["orders-events"].Entry, Files: files}
		},
		"descriptor path in a bundle": func(bs map[string]*bundle.Bundle) {
			bs["orders-http"].Files[DescriptorName] = []byte("x")
		},
	} {
		t.Run(name, func(t *testing.T) {
			desc, bundles := upload(t, example+"/portal.yaml")
			edit(bundles)
			if _, err := RunBundles(desc, bundles, Options{}); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestRunBundlesInvalidDescriptor(t *testing.T) {
	// A descriptor that fails validation is reported as findings, like check.
	r, err := RunBundles([]byte("apiVersion: portal/v1\n"), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(errorsOf(r)) == 0 || r.Findings[0].File != DescriptorName {
		t.Errorf("findings = %+v", r.Findings)
	}
}
