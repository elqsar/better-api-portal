// Package check runs the offline pipeline behind `portal check`: descriptor,
// then each API's spec. It never writes anything.
package check

import (
	"better-api-portal/internal/descriptor"
	"better-api-portal/internal/model"
	"better-api-portal/internal/spec/eventcatalog"
)

// Run checks the descriptor at descPath and the specs it lists. Problems are
// findings; the error is reserved for failures to run the check at all.
func Run(descPath string) ([]model.Finding, error) {
	d, findings, err := descriptor.Load(descPath)
	if err != nil || d == nil {
		return findings, err
	}
	for i, api := range d.APIs {
		if !d.SpecOK(i) {
			continue // already reported: missing, unsupported or of the wrong kind
		}
		specPath := d.SpecPath(i)
		switch api.Kind {
		case descriptor.KindCloudEvents:
			_, fs, err := eventcatalog.Parse(d.Dir, specPath)
			if err != nil {
				return nil, err
			}
			findings = append(findings, fs...)
		case descriptor.KindOpenAPI, descriptor.KindAsyncAPI:
			// Parsed in later milestones.
		}
	}
	return findings, nil
}
