package diff

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/oasdiff/checker"
	oasdiff "github.com/oasdiff/oasdiff/diff"
	"github.com/oasdiff/oasdiff/load"

	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/spec/openapi"
	"github.com/elqsar/better-api-portal/internal/yamldoc"
)

// levelOverrides align oasdiff with rows of the 04-governance OpenAPI table
// where their defaults differ.
var levelOverrides = map[string]checker.Level{
	// Clients may read an optional field; removing it breaks them.
	"response-optional-property-removed": checker.ERR,
	// Only clients with exhaustive switches break.
	"response-property-enum-value-added": checker.WARN,
}

// versioningChecks are oasdiff's own semver gate, which internal/policy
// replaces.
var versioningChecks = map[string]bool{
	checker.APIVersionNotBumpedId:      true,
	checker.APIVersionDecreasedId:      true,
	checker.APIMajorVersionNotBumpedId: true,
}

// OpenAPI compares two versions of an OpenAPI document with oasdiff, whose
// checks are the source of truth for what breaks (04-governance, OpenAPI),
// apart from levelOverrides. Its levels map as ERR → breaking, WARN → warn
// and INFO → additive.
func OpenAPI(old, new *openapi.Result) ([]model.Change, error) {
	var infos [2]*load.SpecInfo
	for i, r := range []*openapi.Result{old, new} {
		t, err := loadOpenAPI(r)
		if err != nil {
			return nil, err
		}
		infos[i] = &load.SpecInfo{Url: r.Doc.Path, Spec: t, Version: t.Info.Version}
	}
	d, sources, err := oasdiff.GetWithOperationsSourcesMap(oasdiff.NewConfig(), infos[0], infos[1])
	if err != nil {
		return nil, fmt.Errorf("diffing %s: %w", new.Doc.Path, err)
	}
	found := checker.CheckBackwardCompatibilityUntilLevel(
		checker.NewConfig(checker.GetAllChecks(), checker.WithSeverityLevels(levelOverrides)), d, sources, checker.INFO)

	l := checker.NewDefaultLocalizer()
	changes := make([]model.Change, 0, len(found))
	for _, fc := range found {
		if versioningChecks[fc.GetId()] {
			continue
		}
		c := model.Change{
			RuleID:  fc.GetId(),
			Kind:    changeKind(fc.GetId()),
			Target:  changeTarget(fc.GetId()),
			Impact:  impact(fc.GetLevel()),
			Message: fc.GetUncolorizedText(l),
		}
		if op := fc.GetOperation(); op != "" {
			c.Type = op + " " + fc.GetPath()
		}
		locate(&c, old, new, fc.GetPath(), fc.GetOperation())
		if c.Impact == model.ImpactBreaking {
			c.ID = changeID("BRK-OA-", c)
		}
		changes = append(changes, c)
	}
	// oasdiff's order follows map iteration; sort for stable output.
	sort.SliceStable(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		return a.Message < b.Message
	})
	return changes, nil
}

// loadOpenAPI loads the document with its $refs resolved. Only files in the
// bundle closure are read, so the loader can't reach anything that
// internal/bundle rejected: remote refs or files outside the root.
func loadOpenAPI(r *openapi.Result) (*openapi3.T, error) {
	c := r.Closure
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, u *url.URL) ([]byte, error) {
		if u.Scheme != "" && u.Scheme != "file" {
			return nil, fmt.Errorf("%s: remote $refs are not followed", u)
		}
		rel, err := filepath.Rel(c.Root, filepath.FromSlash(u.Path))
		if err != nil || c.Docs[filepath.ToSlash(rel)] == nil {
			return nil, fmt.Errorf("%s: not in the spec's file closure", u.Path)
		}
		return os.ReadFile(filepath.Join(c.Root, rel))
	}
	entry := filepath.Join(c.Root, filepath.FromSlash(c.Entry))
	t, err := loader.LoadFromDataWithPath(r.Bytes, &url.URL{Path: filepath.ToSlash(entry)})
	if err != nil {
		return nil, fmt.Errorf("loading %s for the diff: %w", r.Doc.Path, err)
	}
	return t, nil
}

func impact(l checker.Level) model.Impact {
	switch l {
	case checker.ERR:
		return model.ImpactBreaking
	case checker.WARN:
		return model.ImpactWarn
	}
	return model.ImpactAdditive
}

func changeKind(id string) string {
	switch {
	case strings.Contains(id, "-added") || strings.HasSuffix(id, "-add"):
		return "added"
	case strings.Contains(id, "-removed") || strings.HasSuffix(id, "-remove"):
		return "removed"
	}
	return "changed"
}

// changeTarget groups oasdiff's check ids by the part of the API they
// concern.
func changeTarget(id string) string {
	switch {
	case strings.HasPrefix(id, "request-parameter"), strings.HasPrefix(id, "new-request-path-parameter"),
		strings.HasPrefix(id, "new-required-request-parameter"), strings.HasPrefix(id, "new-optional-request-parameter"):
		return "parameter"
	case strings.HasPrefix(id, "request-"), strings.HasPrefix(id, "new-required-request"),
		strings.HasPrefix(id, "new-optional-request"):
		return "request-body"
	case strings.HasPrefix(id, "response-"):
		return "response"
	case strings.HasPrefix(id, "api-"), strings.HasPrefix(id, "endpoint-"):
		return "operation"
	}
	return "metadata"
}

// locate points c at the operation, in the new spec or, when it's gone, in
// the old one. Changes without an operation point at the new document.
func locate(c *model.Change, old, new *openapi.Result, path, method string) {
	docs := []*yamldoc.Doc{new.Doc, old.Doc}
	var ptrs []string
	if path != "" {
		if method != "" {
			ptrs = append(ptrs, yamldoc.Pointer("paths", path, strings.ToLower(method)))
		}
		ptrs = append(ptrs, yamldoc.Pointer("paths", path))
	}
	for _, ptr := range ptrs {
		for _, d := range docs {
			if d.Has(ptr) {
				c.File, c.Pointer, c.Line = d.Path, ptr, d.Line(ptr)
				return
			}
		}
	}
	c.File = docs[0].Path
}
