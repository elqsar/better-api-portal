// Package check runs the offline pipeline behind `portal check`: descriptor,
// then each API's spec, then, given a baseline, the diff and version policy.
// It never writes anything.
package check

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"better-api-portal/internal/bundle"
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
	// Baselines are what the current APIs are diffed against: at most one
	// previous descriptor, whose APIs are matched by id, and any number of
	// "api-id=bundle.tar.zst" for single APIs, which take precedence. None
	// skips the diff.
	Baselines []string
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
	ContentHash     string         `json:"content_hash"`
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
	base, err := loadBaselines(opts.Baselines, d)
	if err != nil {
		return nil, err
	}
	defer base.close()
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
			if result.ContentHash, err = contentHash(d.Dir, res.Spec.Files); err != nil {
				return nil, err
			}
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
			if result.ContentHash, err = contentHash(d.Dir, res.Spec.Files); err != nil {
				return nil, err
			}
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

// baselines are where the APIs' previous versions come from.
type baselines struct {
	desc    *descriptor.Descriptor
	bundles map[string]baseSpec // by API id
	tmp     string              // where bundles are unpacked; removed by close
}

// baseSpec is an API's previous version on disk.
type baseSpec struct {
	Root, Path string // the directory $refs resolve in, and the entry file
	Kind       descriptor.Kind
	Lifecycle  string // unknown ("") for bundles, which carry no metadata
}

// loadBaselines reads the --baseline arguments. Bundles are unpacked into a
// temporary directory, since the parsers read from disk.
func loadBaselines(args []string, d *descriptor.Descriptor) (*baselines, error) {
	b := &baselines{bundles: map[string]baseSpec{}}
	for _, arg := range args {
		id, file, isBundle := strings.Cut(arg, "=")
		if _, err := os.Stat(arg); err == nil || id == "" || strings.ContainsAny(id, `/\`) {
			isBundle = false // a path that happens to contain "="
		}
		if !isBundle {
			if b.desc != nil {
				b.close()
				return nil, fmt.Errorf("baseline: give at most one portal.yaml, got %s and %s", b.desc.Path, arg)
			}
			desc, fs, err := descriptor.Load(arg)
			if err != nil {
				b.close()
				return nil, fmt.Errorf("baseline: %w", err)
			}
			if err := failOnErrors("baseline "+arg, fs); err != nil {
				b.close()
				return nil, err
			}
			b.desc = desc
			continue
		}
		spec, err := b.unpack(id, file, d)
		if err != nil {
			b.close()
			return nil, fmt.Errorf("baseline %s: %w", arg, err)
		}
		b.bundles[id] = spec
	}
	return b, nil
}

func (b *baselines) unpack(id, file string, d *descriptor.Descriptor) (baseSpec, error) {
	if _, dup := b.bundles[id]; dup {
		return baseSpec{}, fmt.Errorf("%s has more than one baseline bundle", id)
	}
	if !slices.ContainsFunc(d.APIs, func(a descriptor.API) bool { return a.ID == id }) {
		return baseSpec{}, fmt.Errorf("%s is not an API in %s", id, d.Path)
	}
	f, err := os.Open(file)
	if err != nil {
		return baseSpec{}, err
	}
	defer f.Close()
	bun, err := bundle.Unpack(f)
	if err != nil {
		return baseSpec{}, err
	}
	if b.tmp == "" {
		if b.tmp, err = os.MkdirTemp("", "portal-baseline-"); err != nil {
			return baseSpec{}, err
		}
	}
	root := filepath.Join(b.tmp, id)
	if err := bun.WriteDir(root); err != nil {
		return baseSpec{}, err
	}
	spec := baseSpec{Root: root, Path: filepath.Join(root, filepath.FromSlash(bun.Entry))}
	kind, finding, err := descriptor.Sniff(spec.Path)
	if err != nil {
		return baseSpec{}, err
	}
	if finding != nil {
		return baseSpec{}, fmt.Errorf("%s: %s", bun.Entry, finding.Message)
	}
	spec.Kind = kind
	return spec, nil
}

func (b *baselines) close() {
	if b.tmp != "" {
		os.RemoveAll(b.tmp)
	}
}

// lookup finds api's previous version. It returns nil when there is nothing
// to compare: no baseline, or the first version of this API. A change of
// kind is a finding.
func (b *baselines) lookup(d *descriptor.Descriptor, api descriptor.API) (*baseSpec, []model.Finding, error) {
	spec, ok := b.bundles[api.ID]
	if !ok && b.desc != nil {
		for j, a := range b.desc.APIs {
			if a.ID != api.ID {
				continue
			}
			if a.Kind == api.Kind && !b.desc.SpecOK(j) {
				return nil, nil, fmt.Errorf("baseline %s: the spec of %s can't be read", b.desc.Path, api.ID)
			}
			spec, ok = baseSpec{Root: b.desc.Dir, Path: b.desc.SpecPath(j), Kind: a.Kind, Lifecycle: a.Lifecycle}, true
		}
	}
	if !ok {
		return nil, nil, nil
	}
	if spec.Kind != api.Kind {
		return nil, []model.Finding{{RuleID: "api-kind-changed", Severity: model.SeverityError, File: d.Path,
			Message: fmt.Sprintf("%s was %s in the baseline and is %s now: API ids are permanent, so publish it under a new id",
				api.ID, spec.Kind, api.Kind)}}, nil
	}
	return &spec, nil, nil
}

// contentHash is the bundle content hash of a spec's files.
func contentHash(root string, files []string) (string, error) {
	b, err := bundle.Read(root, files)
	if err != nil {
		return "", err
	}
	return b.Hash()
}

// compareEvents diffs a parsed event catalogue against its baseline, if the
// baseline has the API, and applies the version policy.
func compareEvents(bases *baselines, d *descriptor.Descriptor, api descriptor.API,
	res *eventcatalog.Result, result *APIResult, acks map[string]string) ([]model.Finding, error) {
	base, kind, err := bases.lookup(d, api)
	if base == nil || err != nil {
		return kind, err
	}
	old, fs, err := eventcatalog.Parse(base.Root, base.Path)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if err := failOnErrors("baseline "+base.Path, fs); err != nil {
		return nil, err
	}

	changes, err := diff.Events(old, res, compat.Mode(api.Compatibility))
	if err != nil {
		return nil, err
	}
	oldHash, err := contentHash(base.Root, old.Spec.Files)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	result.BaselineVersion = old.Spec.Version
	result.Changes = changes
	return policy.Evaluate(policy.Input{
		Version:           res.Spec.Version,
		BaselineVersion:   old.Spec.Version,
		Lifecycle:         api.Lifecycle,
		BaselineLifecycle: base.Lifecycle,
		Changes:           changes,
		SameContent:       oldHash == result.ContentHash,
		Acks:              acks,
		File:              res.Doc.Path,
		VersionLine:       res.Doc.Line("/version"),
	}), nil
}

// compareOpenAPI diffs a parsed OpenAPI document against its baseline, if
// the baseline has the API, and applies the version policy.
func compareOpenAPI(bases *baselines, d *descriptor.Descriptor, api descriptor.API,
	res *openapi.Result, result *APIResult, acks map[string]string) ([]model.Finding, error) {
	base, kind, err := bases.lookup(d, api)
	if base == nil || err != nil {
		return kind, err
	}
	old, fs, err := openapi.Parse(base.Root, base.Path)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if old == nil {
		return nil, fmt.Errorf("baseline %s can't be read as a whole", base.Path)
	}
	if err := failOnErrors("baseline "+base.Path, fs); err != nil {
		return nil, err
	}

	changes, err := diff.OpenAPI(old, res)
	if err != nil {
		return nil, err
	}
	oldHash, err := contentHash(base.Root, old.Spec.Files)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	result.BaselineVersion = old.Spec.Version
	result.Changes = changes
	return policy.Evaluate(policy.Input{
		Version:           res.Spec.Version,
		BaselineVersion:   old.Spec.Version,
		Lifecycle:         api.Lifecycle,
		BaselineLifecycle: base.Lifecycle,
		Changes:           changes,
		SameContent:       oldHash == result.ContentHash,
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
