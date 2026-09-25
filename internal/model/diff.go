package model

// Impact is how a change affects the API's users, which decides the version
// bump it needs.
type Impact string

const (
	ImpactBreaking Impact = "breaking" // needs a new major version
	ImpactWarn     Impact = "warn"     // probably safe, but worth a look; needs a minor
	ImpactAdditive Impact = "additive" // needs a minor
	ImpactDocs     Impact = "docs"     // a patch is enough
)

// Change is one difference between two versions of an API
// (docs/spec/02-domain-model.md, DiffReport).
type Change struct {
	// ID identifies a breaking change so it can be acknowledged. It is
	// derived from the change itself, so identical input gives the same id.
	ID     string `json:"id,omitempty"`
	RuleID string `json:"rule_id"`
	Kind   string `json:"kind"`   // added | removed | changed
	Target string `json:"target"` // message | binding | schema-field | metadata
	// Type is the event type concerned, if any.
	Type string `json:"type,omitempty"`
	// Field is the payload instance path, binding address or extension name.
	Field   string `json:"field,omitempty"`
	Impact  Impact `json:"impact"`
	Message string `json:"message"`
	// File, Pointer and Line locate the change: in the new spec, or in the
	// old one for removals.
	File    string `json:"file,omitempty"`
	Pointer string `json:"pointer,omitempty"`
	Line    int    `json:"line,omitempty"`
}
