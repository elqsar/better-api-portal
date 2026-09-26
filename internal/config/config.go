// Package config loads portal.config.yaml (docs/spec/05-architecture.md
// §Portal configuration), the whole customisation surface of the portal.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path"

	"go.yaml.in/yaml/v3"
)

// Config is the portal configuration. Only the sections the offline check
// needs are modelled so far; the others are ignored until the server needs
// them.
type Config struct {
	Org    Org    `yaml:"org"`
	Teams  []Team `yaml:"teams"`
	Server Server `yaml:"server"`
	CI     CI     `yaml:"ci"`
}

// CI configures how CI jobs authenticate to the portal.
type CI struct {
	// TrustedIssuers are the OIDC issuers whose ID tokens are accepted.
	TrustedIssuers []TrustedIssuer `yaml:"trustedIssuers"`
}

// TrustedIssuer is a CI system whose job ID tokens the portal accepts, such
// as GitHub Actions (https://token.actions.githubusercontent.com) or a GitLab
// instance.
type TrustedIssuer struct {
	Issuer string `yaml:"issuer"`
	// Audience is the aud the job must request; default "api-portal".
	Audience string `yaml:"audience"`
	// RepoClaim names the claim that identifies the repo; default
	// "repository" (GitHub). GitLab's is "project_path".
	RepoClaim string `yaml:"repoClaim"`
	// RepoPrefix is put before the claim to form the repo's CI subject, so
	// two issuers can't claim each other's repos. It must differ between
	// issuers when there are several, e.g. "gitlab:".
	RepoPrefix string `yaml:"repoPrefix"`
	// AllowedRefs are path.Match patterns for the refs that may push, e.g.
	// "refs/heads/main" or "refs/tags/*"; default main and every tag.
	// Other refs can still check and download baselines.
	AllowedRefs []string `yaml:"allowedRefs"`
}

// Defaults for a TrustedIssuer.
const (
	DefaultAudience  = "api-portal"
	DefaultRepoClaim = "repository"
)

// DefaultAllowedRefs is what a TrustedIssuer without allowedRefs allows.
var DefaultAllowedRefs = []string{"refs/heads/main", "refs/tags/*"}

// Server configures `portal serve`.
type Server struct {
	// Listen is the address to listen on; default ":8080".
	Listen string `yaml:"listen"`
	// PublicURL is the portal's base URL, for the links in push results,
	// e.g. "https://api-portal.internal". Without it they are paths.
	PublicURL string `yaml:"publicURL"`
}

type Org struct {
	Name string `yaml:"name"`
	// EventTypePrefix is the prefix every CloudEvents type must start with
	// (rule ce-type-prefix), e.g. "com.acme.".
	EventTypePrefix string `yaml:"eventTypePrefix"`
}

type Team struct {
	Slug      string `yaml:"slug"`
	Name      string `yaml:"name"`
	OIDCGroup string `yaml:"oidcGroup"`
}

// Load reads the configuration at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.CI.normalise(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// normalise fills in the defaults and rejects what can't work.
func (c *CI) normalise() error {
	prefixes := map[string]string{}
	for i := range c.TrustedIssuers {
		t := &c.TrustedIssuers[i]
		u, err := url.Parse(t.Issuer)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("ci.trustedIssuers[%d]: issuer %q is not a URL", i, t.Issuer)
		}
		if t.Audience == "" {
			t.Audience = DefaultAudience
		}
		if t.RepoClaim == "" {
			t.RepoClaim = DefaultRepoClaim
		}
		if len(t.AllowedRefs) == 0 {
			t.AllowedRefs = DefaultAllowedRefs
		}
		for _, p := range t.AllowedRefs {
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("ci.trustedIssuers[%d]: allowedRefs %q: %w", i, p, err)
			}
		}
		if other, ok := prefixes[t.RepoPrefix]; ok {
			return fmt.Errorf("ci.trustedIssuers[%d]: %s and %s share the repoPrefix %q, so either could push to the other's repos",
				i, other, t.Issuer, t.RepoPrefix)
		}
		prefixes[t.RepoPrefix] = t.Issuer
	}
	return nil
}
