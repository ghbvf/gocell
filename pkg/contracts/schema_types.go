// Package contracts defines shared schema and transport types used by both
// kernel/metadata (the YAML metadata model) and pkg/contracttest (the test
// validation helpers). Extracting these types into pkg/ avoids model
// duplication while respecting the layering rule: pkg/ has no kernel/ dependency.
//
// ref: k8s.io/apimachinery — shared types in a standalone base package,
// single dependency direction from higher layers.
package contracts

// HTTPTransport holds transport-level details for HTTP contracts.
// It is optional so legacy HTTP contracts can remain schema-only until migrated.
type HTTPTransport struct {
	Method        string               `yaml:"method"        json:"method"`
	Path          string               `yaml:"path"          json:"path"`
	SuccessStatus int                  `yaml:"successStatus" json:"successStatus"`
	NoContent     bool                 `yaml:"noContent"     json:"noContent"`
	Responses     map[int]HTTPResponse `yaml:"responses,omitempty" json:"responses,omitempty"`
}

// HTTPResponse describes a declared error response for a specific HTTP status code.
// It references a JSON Schema file (relative to the contract directory) that
// describes the error response body.
type HTTPResponse struct {
	Description string `yaml:"description" json:"description"`
	SchemaRef   string `yaml:"schemaRef"   json:"schemaRef"`
}

// SchemaRefs holds JSON Schema file references relative to the contract directory.
// Known keys are request, response, payload, headers; additional string-valued
// keys are captured in Extra to stay compatible with contract.schema.json's
// additionalProperties: {"type":"string"}.
type SchemaRefs struct {
	Request  string            `yaml:"request,omitempty"  json:"request,omitempty"`
	Response string            `yaml:"response,omitempty" json:"response,omitempty"`
	Payload  string            `yaml:"payload,omitempty"  json:"payload,omitempty"`
	Headers  string            `yaml:"headers,omitempty"  json:"headers,omitempty"`
	Extra    map[string]string `yaml:",inline"            json:"-"`
}
