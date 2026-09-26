// Package check runs the offline pipeline behind `portal check`: descriptor,
// then each API's spec, then, given a baseline, the diff and version policy.
// It never writes anything.
package check

import (
	"fmt"
	"maps"
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
	// BaselineBundles are per-API baselines already in memory, as the server
	// has them. An id may not also have a bundle in Baselines.
	BaselineBundles map[string]Baseline
	// Acks maps breaking-change ids to the reason they are acknowledged.
	Acks map[string]string

	// workDir is where baseline bundles are unpacked; empty means the system
	// temporary directory.
	workDir string
}

// Baseline is an API's previous version as a bundle.
type Baseline struct {
	Bundle *bundle.Bundle
	// Lifecycle is the previous version's lifecycle, if known; a bundle
	// doesn't carry it, and without it lifecycle-reversal isn't checked.
	Lifecycle string
}

// Report is the outcome of a check.
type Report struct {
	Descriptor string // the descriptor's path, as given
	Findings   []model.Finding
	APIs       []APIResult // the APIs whose spec was parsed, in descriptor order
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
	r := &Report{Descriptor: descPath, Findings: findings}
	if d == nil {
		return r, nil
	}
	base, err := loadBaselines(opts, d)
	if err != nil {
		return nil, err
	}
	defer base.close()
	var cfg lint.Config
	if opts.Config != nil {
		cfg.EventTypePrefix = opts.Config.Org.EventTypePrefix
		r.Findings = append(r.Findings, unknownOwners(d, opts.Config)...)
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

// unknownOwners reports owners that aren't teams in the configuration, once
// per place they are set. A configuration without teams checks nothing.
func unknownOwners(d *descriptor.Descriptor, cfg *config.Config) []model.Finding {
	if len(cfg.Teams) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, t := range cfg.Teams {
		known[t.Slug] = true
	}
	var fs []model.Finding
	inherited := false
	for i, api := range d.APIs {
		if api.Owner == "" {
			inherited = true
			continue
		}
		if !known[api.Owner] {
			ptr := fmt.Sprintf("/apis/%d/owner", i)
			fs = append(fs, model.Finding{API: api.ID, RuleID: "descriptor-owner-unknown", Severity: model.SeverityError,
				Message: fmt.Sprintf("owner %s is not a team in the portal configuration", api.Owner),
				File:    d.Path, Pointer: ptr, Line: d.Line(ptr)})
		}
	}
	if inherited && !known[d.Owner] {
		fs = append(fs, model.Finding{RuleID: "descriptor-owner-unknown", Severity: model.SeverityError,
			Message: fmt.Sprintf("owner %s is not a team in the portal configuration", d.Owner),
			File:    d.Path, Pointer: "/owner", Line: d.Line("/owner")})
	}
	return fs
}

// baselines are where the APIs' previous versions come from.
type baselines struct {
	desc    *descriptor.Descriptor
	bundles map[string]baseSpec // by API id
	tmp     string              // where bundles are unpacked; removed by close
	workDir string              // where tmp is created; "" for the system default
}

// baseSpec is an API's previous version on disk.
type baseSpec struct {
	Root, Path string // the directory $refs resolve in, and the entry file
	Kind       descriptor.Kind
	Lifecycle  string // unknown ("") for bundles, which carry no metadata
}

// loadBaselines reads the --baseline arguments and the in-memory baselines.
// Bundles are unpacked into a temporary directory, since the parsers read from
// disk.
func loadBaselines(opts Options, d *descriptor.Descriptor) (*baselines, error) {
	b := &baselines{bundles: map[string]baseSpec{}, workDir: opts.workDir}
	for _, arg := range opts.Baselines {
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
		bun, err := readBundle(file)
		if err == nil {
			err = b.add(id, Baseline{Bundle: bun}, d)
		}
		if err != nil {
			b.close()
			return nil, fmt.Errorf("baseline %s: %w", arg, err)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(opts.BaselineBundles)) {
		if err := b.add(id, opts.BaselineBundles[id], d); err != nil {
			b.close()
			return nil, fmt.Errorf("baseline of %s: %w", id, err)
		}
	}
	return b, nil
}

func readBundle(file string) (*bundle.Bundle, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return bundle.Unpack(f)
}

// add unpacks the baseline of API id.
func (b *baselines) add(id string, base Baseline, d *descriptor.Descriptor) error {
	if _, dup := b.bundles[id]; dup {
		return fmt.Errorf("%s has more than one baseline bundle", id)
	}
	if !slices.ContainsFunc(d.APIs, func(a descriptor.API) bool { return a.ID == id }) {
		return fmt.Errorf("%s is not an API in %s", id, d.Path)
	}
	spec, err := b.unpack(id, base.Bundle)
	if err != nil {
		return err
	}
	spec.Lifecycle = base.Lifecycle
	b.bundles[id] = spec
	return nil
}

func (b *baselines) unpack(id string, bun *bundle.Bundle) (baseSpec, error) {
	if b.tmp == "" {
		var err error
		if b.tmp, err = os.MkdirTemp(b.workDir, "portal-baseline-"); err != nil {
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
