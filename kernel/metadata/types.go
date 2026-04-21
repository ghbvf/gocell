// Package metadata defines the Go struct types for GoCell YAML metadata files
// and provides a file-system-based parser to load them into a unified ProjectMeta.
package metadata

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/contracts"
	"gopkg.in/yaml.v3"
)

// CellMeta maps to cells/{id}/cell.yaml.
//
// Dir captures the filesystem directory segment under cells/ as walked by the
// parser. It is populated from the file path, not the YAML, so strict-mode
// governance rules (REF-04) can compare filesystem truth against cell.id
// without being fooled by a path/id split.
type CellMeta struct {
	ID               string         `yaml:"id"`
	Type             string         `yaml:"type"`             // "core"|"edge"|"support"
	ConsistencyLevel string         `yaml:"consistencyLevel"` // "L0"-"L4"
	DurabilityMode   string         `yaml:"durabilityMode"`   // "demo"|"durable" (advisory for L2+)
	Owner            OwnerMeta      `yaml:"owner"`
	Schema           SchemaMeta     `yaml:"schema"`
	Verify           CellVerifyMeta `yaml:"verify"`
	L0Dependencies   []L0DepMeta    `yaml:"l0Dependencies"`
	Dir              string         `yaml:"-"` // directory segment under cells/, set by parser
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
//
// Dir / CellDir record the filesystem directory segments as walked by the
// parser — they are the ground truth for strict-mode governance rules
// (FMT-16, FMT-17, REF-05). Reading these instead of rederiving from the
// map key prevents a path-vs-id split from fooling the validator (e.g. a
// kebab directory paired with a no-dash id in slice.yaml).
type SliceMeta struct {
	ID             string          `yaml:"id"`
	BelongsToCell  string          `yaml:"belongsToCell"`
	ContractUsages []ContractUsage `yaml:"contractUsages"`
	Verify         SliceVerifyMeta `yaml:"verify"`
	AllowedFiles   []string        `yaml:"allowedFiles,omitempty"`
	Dir            string          `yaml:"-"` // slice directory segment, set by parser
	CellDir        string          `yaml:"-"` // parent cell directory segment, set by parser
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
	Kind              string         `yaml:"kind"` // http|event|command|projection
	OwnerCell         string         `yaml:"ownerCell"`
	ConsistencyLevel  string         `yaml:"consistencyLevel"`
	Lifecycle         string         `yaml:"lifecycle"` // draft|active|deprecated
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
	Server  string             `yaml:"server,omitempty"`
	Clients []string           `yaml:"clients,omitempty"`
	HTTP    *HTTPTransportMeta `yaml:"http,omitempty"`
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

// HTTPResponseMeta is a type alias for contracts.HTTPResponse.
// It describes an expected error response for a specific HTTP status code.
type HTTPResponseMeta = contracts.HTTPResponse

// HTTPTransportMeta is a type alias for contracts.HTTPTransport.
// It holds transport-level details for migrated HTTP contracts.
type HTTPTransportMeta = contracts.HTTPTransport

// SchemaRefsMeta is a type alias for contracts.SchemaRefs.
// It holds JSON Schema file references relative to the contract directory.
type SchemaRefsMeta = contracts.SchemaRefs

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
	Mode     string `yaml:"mode"` // "auto"|"manual"
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
	// fileNodes maps each parsed YAML file path (as walked during ParseFS) to its
	// root DocumentNode, enabling validator rules to report precise
	// file:line:column locations. nil when the project was constructed
	// manually (e.g. in tests); callers must tolerate that case.
	// Access via Locate, PrepareFileNode, HasFileNodes.
	fileNodes map[string]*yaml.Node
}

// Locate returns the Position of the YAML value at the given dotted field path
// inside file. Returns a zero Position when any precondition is missing (nil
// receiver, no file nodes, file not found, path unresolvable).
func (pm *ProjectMeta) Locate(file, path string) Position {
	if pm == nil || file == "" || path == "" {
		return Position{}
	}
	if pm.fileNodes == nil {
		return Position{}
	}
	n, ok := pm.fileNodes[file]
	if !ok || n == nil {
		return Position{}
	}
	return Locate(n, path)
}

// setFileNode stores a parsed YAML document node for the given file path.
// It initializes the internal map on first use. Same-package only (parser).
func (pm *ProjectMeta) setFileNode(file string, node *yaml.Node) {
	if pm.fileNodes == nil {
		pm.fileNodes = make(map[string]*yaml.Node)
	}
	pm.fileNodes[file] = node
}

// PrepareFileNode parses yamlSource into a document node and stores it for
// the given file path. This is the public entry point for test setup —
// callers never need to import yaml.v3 directly.
func (pm *ProjectMeta) PrepareFileNode(file string, yamlSource []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(yamlSource, &root); err != nil {
		return fmt.Errorf("metadata: PrepareFileNode %s: %w", file, err)
	}
	pm.setFileNode(file, &root)
	return nil
}

// fileNode returns the parsed YAML document node for the given file path.
// Returns (nil, false) if the file was not parsed or file nodes are not available.
// Same-package only; external callers use Locate.
func (pm *ProjectMeta) fileNode(file string) (*yaml.Node, bool) {
	if pm == nil || pm.fileNodes == nil {
		return nil, false
	}
	n, ok := pm.fileNodes[file]
	return n, ok
}

// HasFileNodes reports whether any file nodes have been stored.
func (pm *ProjectMeta) HasFileNodes() bool {
	if pm == nil {
		return false
	}
	return len(pm.fileNodes) > 0
}
