package model

// Spec is the normalised content of one spec file: one version of one API
// (docs/spec/02-domain-model.md).
type Spec struct {
	Kind        string    `json:"kind"` // openapi | asyncapi | cloudevents
	Title       string    `json:"title"`
	Version     string    `json:"version"`
	Description string    `json:"description,omitempty"`
	Messages    []Message `json:"messages,omitempty"`
	// Schemas holds every schema document the spec uses, keyed by pointer.
	Schemas map[string]*Schema `json:"schemas,omitempty"`
	// Files is the bundle closure: the entry file and every file reachable
	// through $ref, as slash paths relative to the descriptor's directory.
	Files []string `json:"files"`
}

// Role of a message for the API that owns its contract.
type Role string

const (
	RoleProduces Role = "produces"
	RoleReceives Role = "receives"
)

// Message is an event type: a CloudEvents type or an AsyncAPI message.
type Message struct {
	Key         string       `json:"key"` // CE type
	Role        Role         `json:"role"`
	Summary     string       `json:"summary"`
	Description string       `json:"description,omitempty"`
	CE          CEAttributes `json:"ce"`
	// Payload is the pointer of the payload schema, a key of Spec.Schemas
	// optionally followed by a fragment.
	Payload    string    `json:"payload"`
	Bindings   []Binding `json:"bindings"`
	Examples   []Example `json:"examples,omitempty"`
	Deprecated bool      `json:"deprecated,omitempty"`
	// Pointer locates the message in the entry file, for findings.
	Pointer string `json:"pointer"`
	Line    int    `json:"line"` // in the entry file
}

// CEAttributes are the CloudEvents envelope attributes a message declares.
type CEAttributes struct {
	Source          string               `json:"source,omitempty"`
	Subject         string               `json:"subject,omitempty"`
	DataContentType string               `json:"datacontenttype"`
	DataSchemaURI   string               `json:"dataschemauri,omitempty"`
	Extensions      map[string]Extension `json:"extensions,omitempty"`
}

// Extension is a CloudEvents extension attribute.
type Extension struct {
	Required    bool   `json:"required,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// Binding is where a message is carried.
type Binding struct {
	Protocol string         `json:"protocol"` // kafka | nats | sns | sqs | eventbridge | pubsub | servicebus
	Address  string         `json:"address"`  // topic, subject, queue or bus
	Props    map[string]any `json:"props,omitempty"`
	// Pointer locates the binding in the entry file. Bindings inherited from
	// defaults share the pointer of the default.
	Pointer string `json:"pointer"`
}

// Example is a named sample payload.
type Example struct {
	Name string `json:"name,omitempty"`
	Data any    `json:"data"`
}

// Schema is one schema document within a version.
type Schema struct {
	Pointer string `json:"pointer"`
	Draft   string `json:"draft"` // 07 | 2019-09 | 2020-12
	Doc     any    `json:"-"`
}
