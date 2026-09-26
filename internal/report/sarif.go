package report

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"better-api-portal/internal/check"
	"better-api-portal/internal/model"
)

// SARIF 2.1.0, as far as code scanning needs it.
type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name    string      `json:"name"`
	Version string      `json:"version,omitempty"`
	Rules   []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID string `json:"id"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             sarifText         `json:"message"`
	Locations           []sarifLocation   `json:"locations"`
	PartialFingerprints map[string]string `json:"partialFingerprints"`
	Properties          sarifProperties   `json:"properties"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           *sarifRegion  `json:"region,omitempty"`
}

type sarifArtifact struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

type sarifProperties struct {
	API      string `json:"api,omitempty"`
	Pointer  string `json:"pointer,omitempty"`
	ChangeID string `json:"changeId,omitempty"`
}

var sarifLevel = map[model.Severity]string{
	model.SeverityError: "error",
	model.SeverityWarn:  "warning",
	model.SeverityInfo:  "note",
}

// SARIF writes the findings as SARIF 2.1.0 for code scanning. Locations are
// relative to root, the repository checkout. Findings in files outside it,
// such as a baseline's, and findings without a file point at the
// descriptor instead, with the real location in the message. When the
// descriptor is outside root too, locations are absolute file URIs.
func SARIF(w io.Writer, r *check.Report, root string) error {
	ids := map[string]bool{}
	for _, f := range r.Findings {
		ids[f.RuleID] = true
	}
	rules := make([]sarifRule, 0, len(ids))
	for id := range ids {
		rules = append(rules, sarifRule{ID: id})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	index := make(map[string]int, len(rules))
	for i, rule := range rules {
		index[rule.ID] = i
	}

	desc, descInside := artifact(root, r.Descriptor)
	results := make([]sarifResult, 0, len(r.Findings))
	for _, f := range r.Findings {
		res := sarifResult{
			RuleID:              f.RuleID,
			RuleIndex:           index[f.RuleID],
			Level:               sarifLevel[f.Severity],
			Message:             sarifText{f.Message},
			PartialFingerprints: map[string]string{"portal/v1": fingerprint(f)},
			Properties:          sarifProperties{API: f.API, Pointer: f.Pointer, ChangeID: f.ID},
		}
		art, line := desc, 1
		if file, inside := artifact(root, f.File); f.File != "" && (inside || !descInside) {
			art, line = file, f.Line
		} else if f.File != "" {
			where := f.File
			if f.Line > 0 {
				where = fmt.Sprintf("%s:%d", where, f.Line)
			}
			res.Message.Text += " (at " + where + ", outside the checkout)"
		}
		loc := sarifLocation{PhysicalLocation: sarifPhysical{ArtifactLocation: art}}
		if line > 0 {
			loc.PhysicalLocation.Region = &sarifRegion{StartLine: line}
		}
		res.Locations = []sarifLocation{loc}
		results = append(results, res)
	}

	return encode(w, sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool:    sarifTool{Driver: sarifDriver{Name: "portal", Version: toolVersion(), Rules: rules}},
			Results: results,
		}},
	})
}

// artifact locates path: relative to %SRCROOT% when it lies inside root,
// else as an absolute file URI. It reports whether path is inside root.
func artifact(root, path string) (sarifArtifact, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return sarifArtifact{URI: filepath.ToSlash(path)}, false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
		return sarifArtifact{URI: u.String()}, false
	}
	return sarifArtifact{URI: filepath.ToSlash(rel), URIBaseID: "%SRCROOT%"}, true
}

// fingerprint identifies a finding across runs, whatever its line, so code
// scanning keeps tracking an alert when lines above it move.
func fingerprint(f model.Finding) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{f.RuleID, f.API, f.Pointer, f.ID, f.Message}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// toolVersion is the module version the binary was built at, if known. A
// variable so tests can pin it.
var toolVersion = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return ""
}
