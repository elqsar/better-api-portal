// Package check runs the offline pipeline behind `portal check`: descriptor,
// then each API's spec, then, given a baseline, the diff and version policy.
// It never writes anything.
package check

import (
	"fmt"
	"path/filepath"

	"better-api-portal/internal/compat"
	"better-api-portal/internal/config"
	"better-api-portal/internal/descriptor"
	"better-api-portal/internal/diff"
	"better-api-portal/internal/lint"
	"better-api-portal/internal/model"
	"better-api-portal/internal/policy"
	"better-api-portal/internal/spec/eventcatalog"
	"better-api-portal/internal/spec/openapi"
)

// Options configure a check.
type Options struct {
	// Config is the portal configuration; nil skips rules that need it.
	Config *config.Config
	// Baseline is the previous version's descriptor. Its APIs, matched by id,
	// are what the current ones are diffed against. Empty skips the diff.
	Baseline string
	// Acks maps breaking-change ids to the reason they are acknowledged.
	Acks map[string]string
}

// Report is the outcome of a check.
type Report struct {
	Findings []model.Finding
	APIs     []APIResult // the APIs whose spec was parsed, in descriptor order
}

// APIResult summarises one API.
type APIResult struct {
	ID    string `json:"id"`
	Score int    `json:"score"` // lint score; version policy findings don't count
	// Version is the spec's version; BaselineVersion and Changes are set when
	// the API was diffed against a baseline.
	Version         string         `json:"version"`
	BaselineVersion string         `json:"baseline_version,omitempty"`
	Changes         []model.Change `json:"changes,omitempty"`
}

// Run checks the descriptor at descPath and the specs it lists. Problems are
// findings; the error is reserved for failures to run the check at all,
// including a baseline that can't be read.
func Run(descPath string, opts Options) (*Report, error) {
	d, findings, err := descriptor.Load(descPath)
	if err != nil {
		return nil, err
	}
	r := &Report{Findings: findings}
	if d == nil {
		return r, nil
	}
	base, err := loadBaseline(opts.Baseline)
	if err != nil {
		return nil, err
	}
	var cfg lint.Config
	if opts.Config != nil {
		cfg.EventTypePrefix = opts.Config.Org.EventTypePrefix
	}
	for i, api := range d.APIs {
		if !d.SpecOK(i) {
			continue // already reported: missing, unsupported or of the wrong kind
		}
		var fs []model.Finding
		switch api.Kind {
		case descriptor.KindCloudEvents:
			res, parsed, err := eventcatalog.Parse(d.Dir, d.SpecPath(i))
			if err != nil {
				return nil, err
			}
			fs = parsed
			if res == nil {
				break // the catalogue doesn't match its schema: nothing to lint or score
			}
			fs = append(fs, lint.CloudEvents(lint.Target{
				Spec:     res.Spec,
				Payloads: res.Payloads,
				File:     res.Doc.Path,
				Line:     res.Doc.Line,
			}, cfg)...)
			result := APIResult{ID: api.ID, Score: lint.Score(fs), Version: res.Spec.Version}
			gate, err := compareEvents(base, d, api, res, &result, opts.Acks)
			if err != nil {
				return nil, err
			}
			fs = append(fs, gate...)
			r.APIs = append(r.APIs, result)
		case descriptor.KindOpenAPI:
			res, parsed, err := openapi.Parse(d.Dir, d.SpecPath(i))
			if err != nil {
				return nil, err
			}
			fs = parsed
			if res == nil {
				break // the document can't be read as a whole: nothing to lint or score
			}
			var envs []string
			for _, e := range api.Environments {
				if e.URL != "" {
					envs = append(envs, e.URL)
				}
			}
			fs = append(fs, lint.OpenAPI(lint.OpenAPITarget{
				Spec:         res.Spec,
				Doc:          res.Doc,
				Raw:          res.Raw,
				Bytes:        res.Bytes,
				File:         res.Doc.Path,
				Dir:          filepath.Dir(res.Doc.Path),
				Environments: envs,
			}, cfg)...)
			result := APIResult{ID: api.ID, Score: lint.Score(fs), Version: res.Spec.Version}
			if !hasErrors(parsed) { // a spec with broken refs can't be loaded for the diff
				gate, err := compareOpenAPI(base, d, api, res, &result, opts.Acks)
				if err != nil {
					return nil, err
				}
				fs = append(fs, gate...)
			}
			r.APIs = append(r.APIs, result)
		case descriptor.KindAsyncAPI:
			// Parsed in a later milestone.
		}
		for j := range fs {
			fs[j].API = api.ID
		}
		r.Findings = append(r.Findings, fs...)
	}
	return r, nil
}

func loadBaseline(path string) (*descriptor.Descriptor, error) {
	if path == "" {
		return nil, nil
	}
	b, fs, err := descriptor.Load(path)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if err := failOnErrors("baseline "+path, fs); err != nil {
		return nil, err
	}
	return b, nil
}

// baselineAPI finds api in the baseline. It returns -1 when there is nothing
// to compare: no baseline, or the first version of this API. A change of
// kind is a finding.
func baselineAPI(base, d *descriptor.Descriptor, api descriptor.API) (int, []model.Finding, error) {
	if base == nil {
		return -1, nil, nil
	}
	bi := -1
	for j, b := range base.APIs {
		if b.ID == api.ID {
			bi = j
		}
	}
	if bi < 0 {
		return -1, nil, nil
	}
	if bapi := base.APIs[bi]; bapi.Kind != api.Kind {
		return -1, []model.Finding{{RuleID: "api-kind-changed", Severity: model.SeverityError, File: d.Path,
			Message: fmt.Sprintf("%s was %s in the baseline and is %s now: API ids are permanent, so publish it under a new id",
				api.ID, bapi.Kind, api.Kind)}}, nil
	}
	if !base.SpecOK(bi) {
		return -1, nil, fmt.Errorf("baseline %s: the spec of %s can't be read", base.Path, api.ID)
	}
	return bi, nil, nil
}

// compareEvents diffs a parsed event catalogue against its baseline, if the
// baseline has the API, and applies the version policy.
func compareEvents(base *descriptor.Descriptor, d *descriptor.Descriptor, api descriptor.API,
	res *eventcatalog.Result, result *APIResult, acks map[string]string) ([]model.Finding, error) {
	bi, kind, err := baselineAPI(base, d, api)
	if bi < 0 || err != nil {
		return kind, err
	}
	bapi := base.APIs[bi]
	old, fs, err := eventcatalog.Parse(base.Dir, base.SpecPath(bi))
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if err := failOnErrors("baseline "+base.SpecPath(bi), fs); err != nil {
		return nil, err
	}

	changes, err := diff.Events(old, res, compat.Mode(api.Compatibility))
	if err != nil {
		return nil, err
	}
	same, err := diff.SameContent(base.Dir, old.Spec.Files, d.Dir, res.Spec.Files)
	if err != nil {
		return nil, err
	}
	result.BaselineVersion = old.Spec.Version
	result.Changes = changes
	return policy.Evaluate(policy.Input{
		Version:           res.Spec.Version,
		BaselineVersion:   old.Spec.Version,
		Lifecycle:         api.Lifecycle,
		BaselineLifecycle: bapi.Lifecycle,
		Changes:           changes,
		SameContent:       same,
		Acks:              acks,
		File:              res.Doc.Path,
		VersionLine:       res.Doc.Line("/version"),
	}), nil
}

// compareOpenAPI diffs a parsed OpenAPI document against its baseline, if
// the baseline has the API, and applies the version policy.
func compareOpenAPI(base *descriptor.Descriptor, d *descriptor.Descriptor, api descriptor.API,
	res *openapi.Result, result *APIResult, acks map[string]string) ([]model.Finding, error) {
	bi, kind, err := baselineAPI(base, d, api)
	if bi < 0 || err != nil {
		return kind, err
	}
	bapi := base.APIs[bi]
	old, fs, err := openapi.Parse(base.Dir, base.SpecPath(bi))
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if old == nil {
		return nil, fmt.Errorf("baseline %s can't be read as a whole", base.SpecPath(bi))
	}
	if err := failOnErrors("baseline "+base.SpecPath(bi), fs); err != nil {
		return nil, err
	}

	changes, err := diff.OpenAPI(old, res)
	if err != nil {
		return nil, err
	}
	same, err := diff.SameContent(base.Dir, old.Spec.Files, d.Dir, res.Spec.Files)
	if err != nil {
		return nil, err
	}
	result.BaselineVersion = old.Spec.Version
	result.Changes = changes
	return policy.Evaluate(policy.Input{
		Version:           res.Spec.Version,
		BaselineVersion:   old.Spec.Version,
		Lifecycle:         api.Lifecycle,
		BaselineLifecycle: bapi.Lifecycle,
		Changes:           changes,
		SameContent:       same,
		Acks:              acks,
		File:              res.Doc.Path,
		VersionLine:       res.Doc.Line("/info/version"),
	}), nil
}

// failOnErrors turns error findings in the baseline into an error: a broken
// baseline isn't the change's fault, and nothing sound can be compared to it.
func failOnErrors(what string, fs []model.Finding) error {
	for _, f := range fs {
		if f.Severity == model.SeverityError {
			return fmt.Errorf("%s is not valid: %s:%d: %s", what, f.File, f.Line, f.Message)
		}
	}
	return nil
}

// DiffFiles compares two spec files directly, each resolved against its own
// directory. Findings are returned instead of changes when either file can't
// be parsed.
func DiffFiles(oldPath, newPath string, mode compat.Mode) ([]model.Change, []model.Finding, error) {
	paths := [2]string{oldPath, newPath}
	var kinds [2]descriptor.Kind
	var findings []model.Finding
	for i, p := range paths {
		kind, f, err := descriptor.Sniff(p)
		if err != nil {
			return nil, nil, err
		}
		if f != nil {
			findings = append(findings, *f)
			continue
		}
		if kind != descriptor.KindCloudEvents && kind != descriptor.KindOpenAPI {
			return nil, nil, fmt.Errorf("%s: diffing %s specs is not supported yet, only event catalogues and OpenAPI", p, orUnknown(kind))
		}
		kinds[i] = kind
	}
	if len(findings) > 0 {
		return nil, findings, nil
	}
	if kinds[0] != kinds[1] {
		return nil, nil, fmt.Errorf("%s is %s but %s is %s: only specs of the same kind can be compared",
			oldPath, kinds[0], newPath, kinds[1])
	}
	if kinds[0] == descriptor.KindOpenAPI {
		var results [2]*openapi.Result
		for i, p := range paths {
			res, fs, err := openapi.Parse(filepath.Dir(p), p)
			if err != nil {
				return nil, nil, err
			}
			findings = append(findings, fs...)
			results[i] = res
		}
		if results[0] == nil || results[1] == nil || hasErrors(findings) {
			return nil, findings, nil
		}
		changes, err := diff.OpenAPI(results[0], results[1])
		return changes, findings, err
	}
	var results [2]*eventcatalog.Result
	for i, p := range paths {
		res, fs, err := eventcatalog.Parse(filepath.Dir(p), p)
		if err != nil {
			return nil, nil, err
		}
		findings = append(findings, fs...)
		results[i] = res
	}
	if results[0] == nil || results[1] == nil || hasErrors(findings) {
		return nil, findings, nil
	}
	changes, err := diff.Events(results[0], results[1], mode)
	return changes, findings, err
}

func orUnknown(k descriptor.Kind) string {
	if k == "" {
		return "unrecognised"
	}
	return string(k)
}

func hasErrors(fs []model.Finding) bool {
	for _, f := range fs {
		if f.Severity == model.SeverityError {
			return true
		}
	}
	return false
}
