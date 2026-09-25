// Package check runs the offline pipeline behind `portal check`: descriptor,
// then each API's spec. It never writes anything.
package check

import (
	"better-api-portal/internal/config"
	"better-api-portal/internal/descriptor"
	"better-api-portal/internal/lint"
	"better-api-portal/internal/model"
	"better-api-portal/internal/spec/eventcatalog"
)

// Options configure a check.
type Options struct {
	// Config is the portal configuration; nil skips rules that need it.
	Config *config.Config
}

// Report is the outcome of a check.
type Report struct {
	Findings []model.Finding
	APIs     []APIResult // the APIs whose spec was parsed, in descriptor order
}

// APIResult summarises one API.
type APIResult struct {
	ID    string `json:"id"`
	Score int    `json:"score"`
}

// Run checks the descriptor at descPath and the specs it lists. Problems are
// findings; the error is reserved for failures to run the check at all.
func Run(descPath string, opts Options) (*Report, error) {
	d, findings, err := descriptor.Load(descPath)
	if err != nil {
		return nil, err
	}
	r := &Report{Findings: findings}
	if d == nil {
		return r, nil
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
			r.APIs = append(r.APIs, APIResult{ID: api.ID, Score: lint.Score(fs)})
		case descriptor.KindOpenAPI, descriptor.KindAsyncAPI:
			// Parsed in later milestones.
		}
		for j := range fs {
			fs[j].API = api.ID
		}
		r.Findings = append(r.Findings, fs...)
	}
	return r, nil
}
