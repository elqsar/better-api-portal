package policy

import (
	"strings"
	"testing"

	"better-api-portal/internal/model"
)

var (
	breaking = model.Change{ID: "BRK-CE-aaaaaa", RuleID: "compat-required", Impact: model.ImpactBreaking, Message: "x is no longer required"}
	additive = model.Change{RuleID: "ce-message-added", Impact: model.ImpactAdditive, Message: "y was added"}
	warn     = model.Change{RuleID: "ce-source-changed", Impact: model.ImpactWarn, Message: "source changed"}
	docs     = model.Change{RuleID: "ce-docs-changed", Impact: model.ImpactDocs, Message: "summary changed"}
)

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want []string // "severity rule", in order; a message substring may follow after " ~ "
	}{
		{name: "additive with a minor bump (2.3.1 → 2.4.0)",
			in: Input{BaselineVersion: "2.3.1", Version: "2.4.0", Changes: []model.Change{additive}}},
		{name: "docs with a patch bump",
			in: Input{BaselineVersion: "2.3.1", Version: "2.3.2", Changes: []model.Change{docs}}},
		{name: "additive with only a patch bump",
			in:   Input{BaselineVersion: "2.3.1", Version: "2.3.2", Changes: []model.Change{additive}},
			want: []string{"warn semver-minor-expected ~ bump the minor version (2.4.0)"}},
		{name: "warn-impact change with only a patch bump",
			in:   Input{BaselineVersion: "2.3.1", Version: "2.3.2", Changes: []model.Change{warn}},
			want: []string{"warn semver-minor-expected"}},
		{name: "breaking with a minor bump (1.4.0 → 1.5.0)",
			in:   Input{BaselineVersion: "1.4.0", Version: "1.5.0", Changes: []model.Change{breaking}},
			want: []string{"error compat-required ~ needs version 2.0.0 or later: x is no longer required"}},
		{name: "breaking with a patch bump also asks for the minor",
			in:   Input{BaselineVersion: "1.4.0", Version: "1.4.1", Changes: []model.Change{breaking}},
			want: []string{"error compat-required", "warn semver-minor-expected"}},
		{name: "breaking with a major bump (1.4.0 → 2.0.0)",
			in: Input{BaselineVersion: "1.4.0", Version: "2.0.0", Changes: []model.Change{breaking, additive}}},
		{name: "breaking with a minor bump while 0.x (0.3.0 → 0.4.0)",
			in: Input{BaselineVersion: "0.3.0", Version: "0.4.0", Changes: []model.Change{breaking}}},
		{name: "breaking with a patch bump while 0.x",
			in:   Input{BaselineVersion: "0.3.0", Version: "0.3.1", Changes: []model.Change{breaking}},
			want: []string{"error compat-required ~ needs version 0.4.0 or later", "warn semver-minor-expected"}},
		{name: "pre-release skips the gate",
			in: Input{BaselineVersion: "1.4.0", Version: "1.5.0-rc.1", Changes: []model.Change{breaking}}},
		{name: "experimental downgrades breaking to warn",
			in:   Input{BaselineVersion: "1.4.0", Version: "1.5.0", Lifecycle: "experimental", Changes: []model.Change{breaking}},
			want: []string{"warn compat-required ~ allowed while experimental"}},
		{name: "acked breaking change is info",
			in: Input{BaselineVersion: "1.4.0", Version: "1.5.0", Changes: []model.Change{breaking},
				Acks: map[string]string{"BRK-CE-aaaaaa": "never populated"}},
			want: []string{"info compat-required ~ (acknowledged: never populated)"}},
		{name: "ack that matches nothing",
			in: Input{BaselineVersion: "1.4.0", Version: "1.5.0", Changes: []model.Change{additive},
				Acks: map[string]string{"BRK-CE-bbbbbb": "typo"}},
			want: []string{"warn ack-unmatched ~ --ack BRK-CE-bbbbbb matches no breaking change"}},
		{name: "ack of a change the major bump already allows",
			in: Input{BaselineVersion: "1.4.0", Version: "2.0.0", Changes: []model.Change{breaking},
				Acks: map[string]string{"BRK-CE-aaaaaa": "r"}},
			want: []string{"warn ack-unmatched"}},
		{name: "same version, same content",
			in: Input{BaselineVersion: "1.4.0", Version: "1.4.0", SameContent: true}},
		{name: "same version, different content",
			in:   Input{BaselineVersion: "1.4.0", Version: "1.4.0", Changes: []model.Change{breaking}},
			want: []string{"error semver-unchanged ~ still 1.4.0"}},
		{name: "same version, docs-only content change",
			in:   Input{BaselineVersion: "1.4.0", Version: "1.4.0", Changes: []model.Change{docs}},
			want: []string{"error semver-unchanged"}},
		{name: "lower version",
			in:   Input{BaselineVersion: "1.4.0", Version: "1.3.9"},
			want: []string{"error semver-not-increasing ~ 1.3.9 is lower than the baseline 1.4.0"}},
		{name: "lifecycle reversal",
			in:   Input{BaselineVersion: "1.4.0", Version: "1.4.1", Lifecycle: "production", BaselineLifecycle: "deprecated"},
			want: []string{"warn lifecycle-reversal ~ from deprecated to production"}},
		{name: "lifecycle moving forward",
			in: Input{BaselineVersion: "1.4.0", Version: "1.4.1", Lifecycle: "deprecated", BaselineLifecycle: "production"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d findings, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, w := range tt.want {
				head, msg, _ := strings.Cut(w, " ~ ")
				g := got[i]
				if string(g.Severity)+" "+g.RuleID != head || !strings.Contains(g.Message, msg) {
					t.Errorf("finding %d = %s %s: %s\nwant %s", i, g.Severity, g.RuleID, g.Message, w)
				}
			}
		})
	}
}

func TestBreakingFindingCarriesTheChange(t *testing.T) {
	ch := breaking
	ch.File, ch.Pointer, ch.Line = "api/events.yaml", "/messages/0/dataschema", 22
	fs := Evaluate(Input{BaselineVersion: "1.4.0", Version: "1.5.0", Changes: []model.Change{ch}})
	if len(fs) != 1 {
		t.Fatalf("findings = %+v", fs)
	}
	f := fs[0]
	if f.ID != ch.ID || f.File != ch.File || f.Pointer != ch.Pointer || f.Line != 22 {
		t.Errorf("finding = %+v", f)
	}
}
