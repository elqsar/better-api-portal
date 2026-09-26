// Package descriptor parses and validates portal.yaml (docs/spec/03-formats.md §1).
package descriptor

import (
	"path/filepath"

	"better-api-portal/internal/yamldoc"
)

// Descriptor is a parsed portal.yaml.
type Descriptor struct {
	APIVersion string     `yaml:"apiVersion"`
	Owner      string     `yaml:"owner"`
	System     string     `yaml:"system"`
	Links      []Link     `yaml:"links"`
	APIs       []API      `yaml:"apis"`
	Consumes   []Consumes `yaml:"consumes"`

	// Path is the file the descriptor was loaded from; Dir is its directory,
	// against which spec paths are resolved.
	Path string `yaml:"-"`
	Dir  string `yaml:"-"`

	specOK map[int]bool
	doc    *yamldoc.Doc
}

// Line returns the line of the JSON pointer ptr in the descriptor, or 0.
func (d *Descriptor) Line(ptr string) int {
	if d.doc == nil {
		return 0
	}
	return d.doc.Line(ptr)
}

// SpecOK reports whether apis[i]'s spec file exists and is of its kind, so
// it can be parsed.
func (d *Descriptor) SpecOK(i int) bool { return d.specOK[i] }

// SpecPath returns the path of apis[i]'s spec file.
func (d *Descriptor) SpecPath(i int) string {
	return filepath.Join(d.Dir, filepath.FromSlash(d.APIs[i].Spec))
}

// API is one entry of apis[].
type API struct {
	ID            string        `yaml:"id"`
	Kind          Kind          `yaml:"kind"`
	Spec          string        `yaml:"spec"`
	Title         string        `yaml:"title"`
	Owner         string        `yaml:"owner"`
	Lifecycle     string        `yaml:"lifecycle"`
	Sunset        string        `yaml:"sunset"`
	Tags          []string      `yaml:"tags"`
	Links         []Link        `yaml:"links"`
	Compatibility string        `yaml:"compatibility"`
	Ruleset       string        `yaml:"ruleset"`
	Environments  []Environment `yaml:"environments"`
}

// EffectiveOwner returns the per-API owner, falling back to the descriptor's.
func (a API) EffectiveOwner(d *Descriptor) string {
	if a.Owner != "" {
		return a.Owner
	}
	return d.Owner
}

// Kind of spec an API is described by.
type Kind string

const (
	KindOpenAPI     Kind = "openapi"
	KindAsyncAPI    Kind = "asyncapi"
	KindCloudEvents Kind = "cloudevents"
)

type Link struct {
	Title string `yaml:"title"`
	URL   string `yaml:"url"`
}

// Environment is an HTTP base URL or a reference to a configured broker.
type Environment struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	Broker string `yaml:"broker"`
}

// Consumes declares a dependency of every API in the repo on another API.
type Consumes struct {
	API   string   `yaml:"api"`
	Types []string `yaml:"types"`
}
