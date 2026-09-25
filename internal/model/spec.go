package model

// Spec is the normalised content of one spec file: one version of one API
// (docs/spec/02-domain-model.md).
type Spec struct {
	Kind        string    `json:"kind"` // openapi | asyncapi | cloudevents
	Title       string    `json:"title"`
	Version     string    `json:"version"`
	Description string    `json:"description,omitempty"`
	Messages    []Message `json:"messages,omitempty"`
	// Operations and Servers are set for OpenAPI specs.
	Operations []Operation `json:"operations,omitempty"`
	Servers    []string    `json:"servers,omitempty"`
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
	Draft   string `json:"draft"` // 07 | 2019-09 | 2020-12, or oas3.0 for OpenAPI 3.0 schema objects
	Doc     any    `json:"-"`
}

// Operation is an HTTP operation (docs/spec/02-domain-model.md).
type Operation struct {
	Method      string   `json:"method"` // upper case
	Path        string   `json:"path"`
	OperationID string   `json:"operation_id,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	// Security lists the scheme names in effect: the operation's own
	// requirements if it declares any (even an empty list), else the
	// document's. Empty means none.
	Security   []string    `json:"security,omitempty"`
	Parameters []Parameter `json:"parameters,omitempty"`
	// Request maps media types to schema pointers.
	Request map[string]string `json:"request,omitempty"`
	// Responses maps status codes to media types to schema pointers. A
	// response without content has an empty map.
	Responses map[string]map[string]string `json:"responses,omitempty"`
	Pointer   string                       `json:"pointer"` // in the entry file
	Line      int                          `json:"line"`
}

// Parameter is an operation parameter, including those inherited from its
// path.
type Parameter struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required,omitempty"`
	Schema   string `json:"schema,omitempty"` // pointer
}
