package config

import (
	"os"
	"path/filepath"
	"strings"
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
	body := "org: {name: X, eventTypePrefix: com.x.}\nrulesets: {openapi: {default: x}}\n"
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

func TestLoadBrokers(t *testing.T) {
	for name, tc := range map[string]struct {
		body, err string
	}{
		"ok":        {body: "brokers: [{name: k, protocol: kafka, bootstrap: 'k:9093', ui: https://kafka-ui/prod}]"},
		"no name":   {body: "brokers: [{protocol: kafka}]", err: "name is required"},
		"duplicate": {body: "brokers: [{name: k}, {name: k}]", err: "k is configured twice"},
		"bad ui":    {body: "brokers: [{name: k, ui: kafka-ui}]", err: "is not a URL"},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "portal.config.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			c, err := Load(p)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b := c.Broker("k")
			if b == nil || b.Protocol != "kafka" || b.UI != "https://kafka-ui/prod" || b.Settings["bootstrap"] != "k:9093" || c.Broker("x") != nil {
				t.Errorf("broker = %+v", b)
			}
		})
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

func TestLoadCI(t *testing.T) {
	for name, tc := range map[string]struct {
		body, err string
	}{
		"defaults":    {body: "ci: {trustedIssuers: [{issuer: https://token.actions.githubusercontent.com}]}"},
		"not a URL":   {body: "ci: {trustedIssuers: [{issuer: token.actions}]}", err: "is not a URL"},
		"bad pattern": {body: "ci: {trustedIssuers: [{issuer: https://a, allowedRefs: ['refs/[']}]}", err: "allowedRefs"},
		"shared prefix": {
			body: "ci: {trustedIssuers: [{issuer: https://a}, {issuer: https://b}]}",
			err:  `share the repoPrefix ""`,
		},
		"distinct prefixes": {body: "ci: {trustedIssuers: [{issuer: https://a}, {issuer: https://b, repoPrefix: 'gitlab:'}]}"},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "portal.config.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			c, err := Load(p)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ti := c.CI.TrustedIssuers[0]
			if ti.Audience != DefaultAudience || ti.RepoClaim != DefaultRepoClaim || len(ti.AllowedRefs) != 2 {
				t.Errorf("defaults not applied: %+v", ti)
			}
		})
	}
}

func TestLoadOIDC(t *testing.T) {
	p := filepath.Join(t.TempDir(), "portal.config.yaml")
	os.WriteFile(p, []byte("oidc: {issuer: https://login.x, clientId: portal}\nadmins: {oidcGroup: eng-platform}\n"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.OIDC.GroupsClaim != "groups" || c.Admins.OIDCGroup != "eng-platform" {
		t.Errorf("oidc = %+v, admins = %+v", c.OIDC, c.Admins)
	}
	os.WriteFile(p, []byte("oidc: {issuer: https://login.x}\n"), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "clientId") {
		t.Errorf("no client id: %v", err)
	}
}
