package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const example = "../../docs/spec/examples/orders-service"

func run(args ...string) (string, error) {
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestAckNeedsReason(t *testing.T) {
	_, err := run("check", "--descriptor", example+"/portal.yaml", "--ack", "BRK-CE-000000")
	if err == nil || errors.Is(err, errFindings) || !strings.Contains(err.Error(), "--ack-reason") {
		t.Errorf("err = %v", err)
	}
}

func TestDiffExitCodes(t *testing.T) {
	out, err := run("diff", example+"/api/events.yaml", example+"/api/events.yaml")
	if err != nil || strings.TrimSpace(out) != "no changes" {
		t.Errorf("identical: out = %q, err = %v", out, err)
	}
	if _, err := run("diff", "--compatibility", "sideways", example+"/api/events.yaml", example+"/api/events.yaml"); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}
