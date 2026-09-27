// Package policy turns a diff into a verdict: the versioning rules of
// docs/spec/04-governance.md §3, lifecycle effects and acknowledgements.
package policy

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/elqsar/better-api-portal/internal/model"
)

// Input is one API's new version compared with its baseline.
type Input struct {
	Version, BaselineVersion     string // semver, without a "v" prefix
	Lifecycle, BaselineLifecycle string
	Changes                      []model.Change
	// SameContent reports whether the new version's files are byte-identical
	// to the baseline's.
	SameContent bool
	// Acks maps change ids to the reason they are acknowledged.
	Acks map[string]string
	// File is the spec file, and VersionLine the line of its version, where
	// version findings point.
	File        string
	VersionLine int
}

// lifecycle order; moving backwards is allowed but warned about.
var lifecycleRank = map[string]int{"experimental": 0, "production": 1, "deprecated": 2, "retired": 3}

// Evaluate returns the findings of the versioning policy.
func Evaluate(in Input) []model.Finding {
	var fs []model.Finding
	version := func(rule string, sev model.Severity, format string, args ...any) {
		fs = append(fs, model.Finding{RuleID: rule, Severity: sev, Message: fmt.Sprintf(format, args...),
			File: in.File, Pointer: "/version", Line: in.VersionLine})
	}
	v, base := "v"+in.Version, "v"+in.BaselineVersion

	if r, ok := lifecycleRank[in.Lifecycle]; ok && r < lifecycleRank[in.BaselineLifecycle] {
		fs = append(fs, model.Finding{RuleID: "lifecycle-reversal", Severity: model.SeverityWarn,
			Message: fmt.Sprintf("lifecycle moves back from %s to %s", in.BaselineLifecycle, in.Lifecycle)})
	}

	switch c := semver.Compare(v, base); {
	case c == 0:
		if !in.SameContent {
			version("semver-unchanged", model.SeverityError,
				"the content changed but the version is still %s: bump it", in.Version)
		}
		return append(fs, unmatchedAcks(in, nil)...)
	case c < 0:
		version("semver-not-increasing", model.SeverityError,
			"version %s is lower than the baseline %s: versions must increase", in.Version, in.BaselineVersion)
		return append(fs, unmatchedAcks(in, nil)...)
	}

	needed := requiredForBreaking(base)
	majorBumped := semver.Compare(v, needed) >= 0
	prerelease := semver.Prerelease(v) != ""
	matched := map[string]bool{}
	var additive bool
	for _, ch := range in.Changes {
		switch ch.Impact {
		case model.ImpactAdditive, model.ImpactWarn:
			additive = true
		case model.ImpactBreaking:
			additive = true // a breaking change is at least additive for the bump check
			if majorBumped || prerelease {
				continue
			}
			f := model.Finding{RuleID: ch.RuleID, ID: ch.ID, File: ch.File, Pointer: ch.Pointer, Line: ch.Line,
				Severity: model.SeverityError,
				Message:  fmt.Sprintf("breaking change needs version %s or later: %s", strings.TrimPrefix(needed, "v"), ch.Message)}
			if in.Lifecycle == "experimental" {
				f.Severity = model.SeverityWarn
				f.Message += " (allowed while experimental)"
			}
			if reason, ok := in.Acks[ch.ID]; ok {
				matched[ch.ID] = true
				f.Severity = model.SeverityInfo
				f.Message += " (acknowledged: " + reason + ")"
			}
			fs = append(fs, f)
		}
	}
	if additive && !prerelease && semver.MajorMinor(v) == semver.MajorMinor(base) {
		version("semver-minor-expected", model.SeverityWarn,
			"%s changes the contract, not just its docs: bump the minor version (%s)",
			in.Version, strings.TrimPrefix(nextMinor(base), "v"))
	}
	return append(fs, unmatchedAcks(in, matched)...)
}

// requiredForBreaking is the lowest version that may carry breaking changes
// after base: the next major, or the next minor while in 0.x.
func requiredForBreaking(base string) string {
	if semver.Major(base) == "v0" {
		return nextMinor(base)
	}
	var major int
	fmt.Sscanf(semver.Major(base), "v%d", &major)
	return fmt.Sprintf("v%d.0.0", major+1)
}

func nextMinor(base string) string {
	var major, minor int
	fmt.Sscanf(semver.MajorMinor(base), "v%d.%d", &major, &minor)
	return fmt.Sprintf("v%d.%d.0", major, minor+1)
}

// unmatchedAcks warns about acks that matched no breaking change, which is
// most likely a typo or a stale CI setting.
func unmatchedAcks(in Input, matched map[string]bool) []model.Finding {
	var ids []string
	for id := range in.Acks {
		if !matched[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var fs []model.Finding
	for _, id := range ids {
		fs = append(fs, model.Finding{RuleID: "ack-unmatched", Severity: model.SeverityWarn, ID: id,
			Message: fmt.Sprintf("--ack %s matches no breaking change that needs one", id)})
	}
	return fs
}
