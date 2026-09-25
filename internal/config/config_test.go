package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExample(t *testing.T) {
	c, err := Load("../../docs/spec/examples/portal.config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Org.EventTypePrefix != "com.acme." || len(c.Teams) == 0 || c.Teams[0].Slug != "team-orders" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLoadIgnoresUnknownSections(t *testing.T) {
	p := filepath.Join(t.TempDir(), "portal.config.yaml")
	body := "org: {name: X, eventTypePrefix: com.x.}\noidc: {issuer: https://login.x}\nbrokers: [{name: k, protocol: kafka}]\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Org.Name != "X" || c.Org.EventTypePrefix != "com.x." {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLoadMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "portal.config.yaml")
	if err := os.WriteFile(p, []byte("org: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("expected an error")
	}
}
