// Package config loads portal.config.yaml (docs/spec/05-architecture.md
// §Portal configuration), the whole customisation surface of the portal.
package config

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// Config is the portal configuration. Only the sections the offline check
// needs are modelled so far; the others are ignored until the server needs
// them.
type Config struct {
	Org   Org    `yaml:"org"`
	Teams []Team `yaml:"teams"`
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
	return &c, nil
}
