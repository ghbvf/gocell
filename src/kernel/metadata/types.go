// Package metadata defines the Go struct types for GoCell YAML metadata files
// and provides a file-system-based parser to load them into a unified ProjectMeta.
package metadata

// CellMeta maps to cells/{id}/cell.yaml.
type CellMeta struct {
	ID               string        `yaml:"id"`
	Type             string        `yaml:"type"`             // "core"|"edge"|"support"
	ConsistencyLevel string        `yaml:"consistencyLevel"` // "L0"-"L4"
	Owner            OwnerMeta     `yaml:"owner"`
	Schema           SchemaMeta    `yaml:"schema"`
	Verify           CellVerifyMeta `yaml:"verify"`
	L0Dependencies   []L0DepMeta   `yaml:"l0Dependencies"`
}

// OwnerMeta identifies the team responsible for a Cell or Journey.
type OwnerMeta struct {
	Team string `yaml:"team"`
	Role string `yaml:"role"`
}

// SchemaMeta holds the primary data schema reference for a Cell.
type SchemaMeta struct {
	Primary string `yaml:"primary"`
}

// CellVerifyMeta holds structured verify refs for a Cell.
// Smoke refs use the format: smoke.{cellID}.{suffix}
type CellVerifyMeta struct {
	Smoke []string `yaml:"smoke"`
}

// L0DepMeta declares a direct dependency on an L0 (LocalOnly) Cell.
type L0DepMeta struct {
	Cell   string `yaml:"cell"`
	Reason string `yaml:"reason"`
}

// SliceMeta maps to cells/{cell-id}/slices/{slice-id}/slice.yaml.
type SliceMeta struct {
	ID             string          `yaml:"id"`
	BelongsToCell  string          `yaml:"belongsToCell"`
	ContractUsages []ContractUsage `yaml:"contractUsages"`
	Verify         SliceVerifyMeta `yaml:"verify"`
}

// ContractUsage declares a Slice's participation in a Contract.
type ContractUsage struct {
	Contract string `yaml:"contract"`
	Role     string `yaml:"role"` // serve|call|publish|subscribe|handle|invoke|provide|read
}

// SliceVerifyMeta holds verification requirements for a Slice.
type SliceVerifyMeta struct {
	Unit     []string     `yaml:"unit"`
	Contract []string     `yaml:"contract"`
	Waivers  []WaiverMeta `yaml:"waivers"`
}

// WaiverMeta records a temporary exemption from a contract verification.
type WaiverMeta struct {
	Contract  string `yaml:"contract"`
	Owner     string `yaml:"owner"`
	Reason    string `yaml:"reason"`
	ExpiresAt string `yaml:"expiresAt"`
}

// ContractMeta maps to contracts/{kind}/{domain...}/{version}/contract.yaml.
type ContractMeta struct {
	ID                string         `yaml:"id"`
	Kind              string         `yaml:"kind"`                        // http|event|command|projection
	OwnerCell         string         `yaml:"ownerCell"`
	ConsistencyLevel  string         `yaml:"consistencyLevel"`
	Lifecycle         string         `yaml:"lifecycle"`                   // draft|active|deprecated
	Endpoints         EndpointsMeta  `yaml:"endpoints"`
	SchemaRefs        SchemaRefsMeta `yaml:"schemaRefs,omitempty"`
	Replayable        *bool          `yaml:"replayable,omitempty"`
	IdempotencyKey    string         `yaml:"idempotencyKey,omitempty"`
	DeliverySemantics string         `yaml:"deliverySemantics,omitempty"`
	Description       string         `yaml:"description,omitempty"`
}

// ProviderEndpoint returns the provider cell/actor ID for this contract
// based on its Kind. Returns "" if Kind is unknown or provider is unset.
func (c *ContractMeta) ProviderEndpoint() string {
	switch c.Kind {
	case "http":
		return c.Endpoints.Server
	case "event":
		return c.Endpoints.Publisher
	case "command":
		return c.Endpoints.Handler
	case "projection":
		return c.Endpoints.Provider
	default:
		return ""
	}
}

// EndpointsMeta holds the kind-specific endpoint fields for a Contract.
// Only the fields relevant to the contract's Kind are populated.
type EndpointsMeta struct {
	// HTTP
	Server  string   `yaml:"server,omitempty"`
	Clients []string `yaml:"clients,omitempty"`
	// Event
	Publisher   string   `yaml:"publisher,omitempty"`
	Subscribers []string `yaml:"subscribers,omitempty"`
	// Command
	Handler  string   `yaml:"handler,omitempty"`
	Invokers []string `yaml:"invokers,omitempty"`
	// Projection
	Provider string   `yaml:"provider,omitempty"`
	Readers  []string `yaml:"readers,omitempty"`
}

// SchemaRefsMeta holds JSON Schema file references relative to the contract directory.
// Known keys are request, response, payload, headers; additional string-valued keys
// are captured in Extra to stay compatible with contract.schema.json's
// additionalProperties: {"type":"string"}.
type SchemaRefsMeta struct {
	Request  string            `yaml:"request,omitempty"`
	Response string            `yaml:"response,omitempty"`
	Payload  string            `yaml:"payload,omitempty"`
	Headers  string            `yaml:"headers,omitempty"`
	Extra    map[string]string `yaml:",inline"`
}

// JourneyMeta maps to journeys/J-*.yaml.
type JourneyMeta struct {
	ID           string          `yaml:"id"`
	Goal         string          `yaml:"goal"`
	Owner        OwnerMeta       `yaml:"owner"`
	Cells        []string        `yaml:"cells"`
	Contracts    []string        `yaml:"contracts"`
	PassCriteria []PassCriterion `yaml:"passCriteria"`
}

// PassCriterion is a single acceptance criterion within a Journey.
type PassCriterion struct {
	Text     string `yaml:"text"`
	Mode     string `yaml:"mode"`               // "auto"|"manual"
	CheckRef string `yaml:"checkRef,omitempty"`
}

// AssemblyMeta maps to assemblies/{id}/assembly.yaml.
type AssemblyMeta struct {
	ID    string    `yaml:"id"`
	Cells []string  `yaml:"cells"`
	Build BuildMeta `yaml:"build"`
}

// BuildMeta holds the build configuration for an Assembly.
type BuildMeta struct {
	Entrypoint     string `yaml:"entrypoint"`
	Binary         string `yaml:"binary"`
	DeployTemplate string `yaml:"deployTemplate"`
}

// StatusBoardEntry maps to a single entry in journeys/status-board.yaml.
type StatusBoardEntry struct {
	JourneyID string `yaml:"journeyId"`
	State     string `yaml:"state"`
	Risk      string `yaml:"risk"`
	Blocker   string `yaml:"blocker"`
	UpdatedAt string `yaml:"updatedAt"`
}

// ActorMeta maps to a single entry in actors.yaml.
type ActorMeta struct {
	ID                  string `yaml:"id"`
	Type                string `yaml:"type"`
	MaxConsistencyLevel string `yaml:"maxConsistencyLevel"`
}

// ProjectMeta holds all parsed metadata for the entire project.
type ProjectMeta struct {
	Cells       map[string]*CellMeta     // keyed by cell ID
	Slices      map[string]*SliceMeta    // keyed by "cellID/sliceID"
	Contracts   map[string]*ContractMeta // keyed by contract ID
	Journeys    map[string]*JourneyMeta  // keyed by journey ID
	Assemblies  map[string]*AssemblyMeta // keyed by assembly ID
	StatusBoard []StatusBoardEntry
	Actors      []ActorMeta
}
