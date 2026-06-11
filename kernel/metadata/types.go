// Package metadata defines the Go struct types for GoCell YAML metadata files
// and provides a file-system-based parser to load them into a unified ProjectMeta.
package metadata

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// CellMeta maps to cells/{id}/cell.yaml.
//
// Dir captures the filesystem directory segment under cells/ as walked by the
// parser. It is populated from the file path, not the YAML, so strict-mode
// governance rules (REF-04) can compare filesystem truth against cell.id
// without being fooled by a path/id split.
type CellMeta struct {
	ID               string `yaml:"id"`
	Type             string `yaml:"type"`             // "core"|"edge"|"support"
	ConsistencyLevel string `yaml:"consistencyLevel"` // "L0"-"L4"
	DurabilityMode   string `yaml:"durabilityMode"`   // "demo"|"durable" (advisory for L2+)
	// Lifecycle is the governance maturity phase
	// (experimental|candidate|asset|maintenance|retired). Optional; empty
	// defaults to experimental at BaseCell construction. Distinct from the
	// runtime cellState and from a Contract's draft/active/deprecated lifecycle
	// — see kernel/cellvocab.CellLifecycle. The parser stays lenient (membership
	// is enforced by cell.schema.json enum + governance CELL-LIFECYCLE-01 +
	// NewBaseCell's ParseCellLifecycle), matching the durabilityMode/requires
	// posture.
	Lifecycle      string         `yaml:"lifecycle,omitempty"`
	Owner          OwnerMeta      `yaml:"owner"`
	Schema         SchemaMeta     `yaml:"schema"`
	Verify         CellVerifyMeta `yaml:"verify"`
	L0Dependencies []L0DepMeta    `yaml:"l0Dependencies"`
	// GoStructName is a schema extension consumed by tools/codegen — kernel
	// itself does not interpret its value. Cells that opt into K#04 codegen
	// MUST set this; non-codegen cells leave it empty. There is no reliable
	// automatic mapping from a lowercased cell id to its conventional CamelCase
	// Go type (e.g. "ordercell" → "OrderCell"), which is why explicit
	// declaration is required.
	GoStructName GoIdentifier `yaml:"goStructName,omitempty"`
	// Requires lists the assembly-level shared infrastructure capabilities this
	// cell consumes (postgres / redis / rabbitmq). It is the authoritative single
	// source: the owning assembly's provisioned capability set is the derived
	// union of its cells' Requires (see kernel/assembly.GenerateModulesGen). The
	// closed value set is mirrored by CapabilityEnum + runtime/capability.Kind +
	// cell.schema.json `requires` enum; unknown/duplicate values are rejected by
	// `gocell validate` governance rule FMT-36 (validation-time, not by ParseFS —
	// the parser stays lenient, matching Kubernetes admission-layer validation).
	// "Requires" names what the cell consumes; it declares intent, not a uniform
	// hardness contract. Whether a listed capability is hard-required or
	// best-effort is decided per kind by the provisioner (cmd/corebundle
	// provisionCapabilities): postgres is hard-required in non-memory storage
	// modes (absence fails fast at the cell's nil-guard); redis is
	// provision-if-available (absence degrades gracefully — e.g. accesscore's
	// AUTH-CACHE-01 session cache is skipped when no redis client is configured).
	Requires []string `yaml:"requires,omitempty"`
	Dir      string   `yaml:"-"` // directory segment under cells/, set by parser
	File     string   `yaml:"-"` // parsed cell.yaml path relative to project root
}

// Clone returns a deep copy of c, independently owning every slice and
// nested struct. Used by kernel/cell.BaseCell construction and by
// kernel/registry.CellRegistry.Get to prevent caller mutation from
// leaking into shared state.
//
// nil receiver returns nil.
func (c *CellMeta) Clone() *CellMeta {
	if c == nil {
		return nil
	}
	cp := *c
	cp.L0Dependencies = append([]L0DepMeta(nil), c.L0Dependencies...)
	cp.Verify.Smoke = append([]string(nil), c.Verify.Smoke...)
	cp.Requires = append([]string(nil), c.Requires...)
	return &cp
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
// Smoke refs use the format: smoke.{cellID}.{suffix}.
type CellVerifyMeta struct {
	Smoke []string `yaml:"smoke"`
}

// L0DepMeta declares a direct dependency on an L0 (LocalOnly) Cell.
//
// L0 Cell status: sanctioned future extension point
//
// L0 (LocalOnly, pure-compute library cell) is a first-class member of the
// consistency-level model (L0-L4). An L0 Cell has no state machine, no
// outbox, and no contracts; it exposes shared pure-computation logic (e.g.
// hashing, validation algorithms, encoding helpers) that other cells in the
// same Assembly may import directly.
//
// As of this writing there is no L0 Cell instance in the platform cells
// (accesscore / auditcore / configcore). Every platform cell.yaml therefore
// carries `l0Dependencies: []` — this empty slice is expected and correct;
// it is not a schema defect or dead-code path.
//
// When a future L0 Cell is created (e.g. a shared pure-compute package
// elevated to a first-class Cell), consuming cells would add an entry here:
//
//	l0Dependencies:
//	  - cell: sharedcrypto
//	    reason: deterministic hashing for cursor tokens
//
// Decision record: C-06-L0-CELL-DECISION — user chose doc-only (option b)
// over elevating an existing package to a concrete L0 instance (option a).
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
	ID            string `yaml:"id"`
	BelongsToCell string `yaml:"belongsToCell"`
	// ConsistencyLevel: "L0"-"L4"; required (parser rejects empty);
	// MUST be ≤ cell.ConsistencyLevel. slice.yaml is the SoR for slice
	// level (codegen funnel projects to slice_gen.go.sliceMeta);
	// inheritance from cell.consistencyLevel is no longer supported.
	ConsistencyLevel string `yaml:"consistencyLevel"`
	// Lifecycle is the slice's governance maturity phase
	// (experimental|candidate|asset|maintenance|retired). Optional; MUST be ≤ the
	// parent cell's lifecycle (governance CELL-LIFECYCLE-01). See
	// kernel/cellvocab.CellLifecycle.
	Lifecycle      string          `yaml:"lifecycle,omitempty"`
	ContractUsages []ContractUsage `yaml:"contractUsages"`
	Verify         SliceVerifyMeta `yaml:"verify"`
	AllowedFiles   []string        `yaml:"allowedFiles,omitempty"`
	Dir            string          `yaml:"-"` // slice directory segment, set by parser
	CellDir        string          `yaml:"-"` // parent cell directory segment, set by parser
	File           string          `yaml:"-"` // parsed slice.yaml path relative to project root
}

// Clone returns a deep copy of s, independently owning every slice and
// nested struct. Used by tools/codegen-generated SliceMetadata() accessors and
// by kernel/cell.NewBaseSliceFromMeta to prevent caller mutation from leaking
// into shared state — mirroring the K8s zz_generated.deepcopy.go pattern.
//
// nil receiver returns nil.
func (s *SliceMeta) Clone() *SliceMeta {
	if s == nil {
		return nil
	}
	cp := *s
	// ContractUsages: independent copy
	cp.ContractUsages = append([]ContractUsage(nil), s.ContractUsages...)
	// Verify: deep copy nested slices
	cp.Verify.Unit = append([]string(nil), s.Verify.Unit...)
	cp.Verify.Contract = append([]string(nil), s.Verify.Contract...)
	cp.Verify.Waivers = append([]WaiverMeta(nil), s.Verify.Waivers...)
	// AllowedFiles: independent copy
	cp.AllowedFiles = append([]string(nil), s.AllowedFiles...)
	return &cp
}

// ContractUsage declares a Slice's participation in a Contract.
type ContractUsage struct {
	Contract string `yaml:"contract"`
	Role     string `yaml:"role"` // serve|call|publish|subscribe|handle|invoke|provide|read|webhook-receive|webhook-dispatch
	// Handler is the consumer handler method name on the slice's service/consumer
	// struct field; REQUIRED for role=subscribe and role=webhook-receive, forbidden
	// otherwise (including role=webhook-dispatch). Single source for the
	// reg.Subscribe / reg.RegisterWebhookReceiver handler expression that cellgen emits.
	Handler string `yaml:"handler,omitempty"`
	// Group is the broker consumer group for role=subscribe; optional, defaults to
	// the owning cell ID when empty. Forbidden for non-subscribe roles (including
	// webhook-receive and webhook-dispatch).
	Group string `yaml:"group,omitempty"`
	// Field optionally names the cell-struct field holding the consumer slice's
	// service, disambiguating slices that own more than one *sliceID.T field
	// (e.g. a route Handler plus a subscribe Consumer or a webhook receiver).
	// Optional for role=subscribe, role=webhook-receive, and role=webhook-dispatch
	// (cellgen resolves by package convention when empty); forbidden for all other
	// roles.
	Field string `yaml:"field,omitempty"`
	// SourceID is the secret-isolation key identifying the webhook source (e.g.
	// "stripe", "shopify"). REQUIRED for role=webhook-receive and
	// role=webhook-dispatch; forbidden for all other roles. For webhook-receive,
	// this value must equal contract.endpoints.inbound.sourceID.
	SourceID string `yaml:"sourceID,omitempty"`
	// TargetSelector is the target-URL selector method name used by the
	// dispatcher to resolve the outbound webhook URL at runtime. REQUIRED for
	// role=webhook-dispatch; forbidden for all other roles.
	TargetSelector string `yaml:"targetSelector,omitempty"`
	// Projection, when set on a role=subscribe CU, makes cellgen emit
	// reg.RegisterProjection (an L3 CQRS projection) instead of reg.Subscribe.
	// The value is the ProjectionID — half of the checkpoint key
	// (cellID, projectionID). MUST be a snake_case probe-name identifier:
	// ^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$. Optional; forbidden on every
	// non-subscribe role.
	Projection string `yaml:"projection,omitempty"`
	// OnReset names the optional rebuild Reset-phase hook method on the same
	// slice service field as Handler (cell.ProjectionResetHook). Only meaningful
	// when Projection is set; exported Go identifier (^[A-Z][A-Za-z0-9_]*$).
	// Optional; subscribe-only. Forbidden when Projection is empty AND forbidden
	// when ProjectionSource is "saga-journal" (the saga-journal path has no rebuild
	// — EPIC #1609 PR-05).
	OnReset string `yaml:"onReset,omitempty"`
	// ProjectionSource selects which input stream feeds the projection (EPIC #1609
	// PR-05). REQUIRED whenever Projection is set; forbidden otherwise. Values:
	//   - "outbox": the named event contract's outbox topic (contract.kind=event).
	//   - "saga-journal": the GLOBAL saga journal (stream saga.journal.v1). The
	//     contract MUST be kind=saga, but the anchor is for LINEAGE/governance only
	//     — it is NOT a runtime filter: the saga-journal source is global (reads
	//     every saga instance/type), so the Apply hook folds the whole journal and
	//     must itself filter if it cares about one saga (same coarse, existence-only
	//     semantics as PROJECTION-PROVIDE-NEEDS-WRITE-CU-01).
	// cellgen branches on this: "outbox" emits <eventpkg>.NewProjectionRequest;
	// "saga-journal" emits cell.NewSagaJournalProjectionRequest.
	ProjectionSource string `yaml:"projectionSource,omitempty"`
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
	ID   string `yaml:"id"`
	Kind string `yaml:"kind"` // http|event|command|projection|webhook|grpc|saga
	// Transports is the non-empty SET of wire transports this contract is
	// sanctioned to bind to (e.g. [amqp, mqtt] for an event published over both
	// brokers). When the contract.yaml omits `transports:`, parser.parseContract
	// defaults it per kind (event/command→[amqp], http/webhook→[http],
	// grpc→[grpc], projection/saga→[internal]) so every existing contract stays
	// byte-identical. Each member MUST be one of cellvocab.AllTransports();
	// kind↔transport compatibility is enforced by governance FMT-39. contractgen
	// derives the generated ContractSpec.Transport as the primary (Transports[0])
	// and emits the full exported set for multi-transport contracts.
	// ref: asyncapi/spec channel.servers (transport set); k8s SetDefaults (per-kind default).
	Transports       []string `yaml:"transports,omitempty"`
	OwnerCell        string   `yaml:"ownerCell"`
	ConsistencyLevel string   `yaml:"consistencyLevel"`
	Lifecycle        string   `yaml:"lifecycle"` // draft|active|deprecated
	// Triggers lists the outbox event topics emitted by this contract's owner
	// cell when the HTTP handler succeeds. Required for L2+ HTTP contracts.
	// Each value MUST be a string literal or named constant (dto.TopicX /
	// domain.TopicX); dynamic expressions (fmt.Sprintf, variables) are rejected
	// by CONTRACT-CONSISTENCY-EMIT-01. Validated bidirectionally against
	// outbox.Emit call sites in cells/<ownerCell>/slices/**/service.go.
	Triggers   []string       `yaml:"triggers,omitempty"`
	Endpoints  EndpointsMeta  `yaml:"endpoints"`
	SchemaRefs SchemaRefsMeta `yaml:"schemaRefs,omitempty"`
	// Saga carries the orchestration definition for kind=saga contracts (steps,
	// timeout, compensationOrder). Nil for all other kinds. Parsed under strict
	// KnownFields decode; the declarative mirror lives in
	// schemas/contract.schema.json. contractgen derives typed step I/O structs +
	// an Impl interface + BuildDefinition/Register from it (PR-07).
	Saga              *SagaMeta `yaml:"saga,omitempty"`
	Replayable        *bool     `yaml:"replayable,omitempty"`
	IdempotencyKey    string    `yaml:"idempotencyKey,omitempty"`
	DeliverySemantics string    `yaml:"deliverySemantics,omitempty"`
	// Codegen opts the contract into `gocell generate contract` output.
	// When true, contractgen produces types_gen.go / iface_gen.go (and
	// handler_gen.go for kind=http) under generated/contracts/<kind>/<...>/v<N>/.
	//
	// K#09 funnel: parser.parseContract defaults this to true when the
	// contract.yaml omits the `codegen:` key (detected via yaml AST in
	// contractYAMLHasKey). Explicit `codegen: false` is the only way to
	// opt out. Scaffold output omits the field entirely so the funnel is
	// the single source of truth (INVARIANT SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01).
	Codegen bool `yaml:"codegen,omitempty"`
	// Direction is the webhook flow direction: "inbound" (external → cell) or
	// "outbound" (cell → external). Only populated for kind=webhook.
	Direction string `yaml:"direction,omitempty"`
	// Signature holds the webhook signature verification metadata. Only
	// populated for kind=webhook contracts.
	Signature *WebhookSignatureMeta `yaml:"signature,omitempty"`
	// Payload holds webhook payload constraints. Only populated for
	// kind=webhook contracts.
	Payload *WebhookPayloadMeta `yaml:"payload,omitempty"`
	// Description / DeprecatedAt are documentation only — excluded from
	// structural fingerprint via fingerprint:"-".
	Description  string `yaml:"description,omitempty" fingerprint:"-"`
	DeprecatedAt string `yaml:"deprecatedAt,omitempty" fingerprint:"-"`
	// Dir / File are parsed-time fields tracking contract.yaml location relative
	// to the project root; never persisted, hence yaml:"-".
	Dir  string `yaml:"-" fingerprint:"-"`
	File string `yaml:"-" fingerprint:"-"`
}

// ProviderEndpoint returns the provider cell/actor ID for this contract
// based on its Kind. Returns "" if Kind is unknown or provider is unset.
//
// For kind=webhook: returns the explicitly declared ownerCell. Webhook
// contracts must declare ownerCell explicitly (see contracts/webhook/README.md).
// Unlike other kinds, the webhook provider cannot be derived from
// Endpoints.Receivers or Endpoints.Dispatchers at parse time because those
// fields are populated by deriveWebhookEndpoints, which runs after parseContract
// calls ProviderEndpoint for the G-7 ownerCell auto-derivation. When ownerCell
// is set, the G-7 if-check in parseContract (if m.OwnerCell == "") is skipped
// correctly; when ownerCell is empty, ProviderEndpoint returns "" and ownerCell
// remains empty (governance rules will flag the omission).
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
	case "webhook":
		// Webhook provider is the explicitly-declared ownerCell.
		// Receivers/Dispatchers are derived fields populated after parse time
		// and must not be consulted here.
		return c.OwnerCell
	case "grpc":
		// gRPC mirrors http: provider is endpoints.server.
		return c.Endpoints.Server
	case "saga":
		// Saga's provider is the orchestrating cell in endpoints.server.
		return c.Endpoints.Server
	default:
		return ""
	}
}

// EndpointsMeta holds the kind-specific endpoint fields for a Contract.
// Only the fields relevant to the contract's Kind are populated.
type EndpointsMeta struct {
	// HTTP (also reused by gRPC: provider=server, consumers=clients)
	Server  string             `yaml:"server,omitempty"`
	Clients []string           `yaml:"clients,omitempty"`
	HTTP    *HTTPTransportMeta `yaml:"http,omitempty"`
	// gRPC transport subtree (parallel to HTTP); server/clients above carry
	// the provider/consumer cells.
	GRPC *GRPCTransportMeta `yaml:"grpc,omitempty"`
	// Event
	Publisher        string   `yaml:"publisher,omitempty"`
	ActorSubscribers []string `yaml:"actorSubscribers,omitempty"`
	// Subscribers is derived by deriveEventSubscribers (parser post-process):
	// union of ActorSubscribers + cell IDs from slice.yaml contractUsages[role=subscribe].
	// yaml:"-" means hand-written "subscribers:" in YAML is rejected by KnownFields strict decode.
	Subscribers []string `yaml:"-"`
	// Command
	Handler  string   `yaml:"handler,omitempty"`
	Invokers []string `yaml:"invokers,omitempty"`
	// Projection
	Provider string   `yaml:"provider,omitempty"`
	Readers  []string `yaml:"readers,omitempty"`
	// Webhook (kind=webhook only)
	Inbound *WebhookInboundMeta `yaml:"inbound,omitempty"`
	// Receivers is derived by deriveWebhookEndpoints (parser post-process): cell
	// IDs from slice.yaml contractUsages[role=webhook-receive].belongsToCell,
	// deduped and sorted alphabetically. yaml:"-" means hand-written "receivers:"
	// in YAML is rejected by KnownFields strict decode — mirrors the Subscribers
	// field pattern.
	Receivers []string `yaml:"-"`
	// Dispatchers is derived by deriveWebhookEndpoints (parser post-process): cell
	// IDs from slice.yaml contractUsages[role=webhook-dispatch].belongsToCell,
	// deduped and sorted alphabetically. yaml:"-" means hand-written "dispatchers:"
	// in YAML is rejected by KnownFields strict decode — mirrors the Subscribers
	// field pattern.
	Dispatchers []string `yaml:"-"`
}

// WebhookSignatureMeta holds HMAC signature verification parameters for an
// inbound webhook contract.
type WebhookSignatureMeta struct {
	// Algorithm is the HMAC algorithm used to verify the webhook signature
	// (e.g. "hmac-sha256").
	Algorithm string `yaml:"algorithm"`
	// ToleranceSeconds is the bidirectional time window (in seconds) for
	// replay-attack prevention. Requests whose timestamp differs from server
	// time by more than this value in either direction are rejected. A value
	// of 0 disables time-window checking (not recommended for production).
	ToleranceSeconds int `yaml:"toleranceSeconds,omitempty"`
	// DeliveryIDHeader is the HTTP header carrying the unique delivery ID
	// (e.g. "svix-id"). Used as part of the signed string to prevent
	// cross-delivery replay attacks.
	DeliveryIDHeader string `yaml:"deliveryIDHeader"`
	// TimestampHeader is the HTTP header carrying the Unix timestamp of the
	// delivery (e.g. "svix-timestamp"). Must be within ToleranceSeconds of
	// server time.
	TimestampHeader string `yaml:"timestampHeader"`
	// SignatureHeader is the HTTP header carrying the HMAC signature value
	// (e.g. "svix-signature").
	SignatureHeader string `yaml:"signatureHeader"`
	// SignedStringForm is a template for constructing the signed string from
	// delivery fields. Template variables: {deliveryID} (value of
	// DeliveryIDHeader), {timestamp} (value of TimestampHeader), {body}
	// (raw request body). Example: "{deliveryID}.{timestamp}.{body}".
	SignedStringForm string `yaml:"signedStringForm"`
}

// WebhookPayloadMeta holds payload constraints for a webhook contract.
type WebhookPayloadMeta struct {
	// SchemaRef is an optional path to a JSON Schema file (relative to the
	// contract.yaml directory) that validates the webhook payload body.
	SchemaRef string `yaml:"schemaRef,omitempty"`
	// ContentType is the expected MIME type of the webhook payload
	// (e.g. "application/json"). Runtime rejects requests with a mismatched
	// Content-Type header.
	ContentType string `yaml:"contentType,omitempty"`
	// MaxBodyBytes is the maximum allowed size (in bytes) of the webhook
	// request body. Runtime rejects requests exceeding this limit with 413.
	// A value of 0 means no limit (not recommended for production).
	MaxBodyBytes int64 `yaml:"maxBodyBytes,omitempty"`
}

// WebhookInboundMeta holds inbound-specific routing fields for a webhook
// contract (direction=inbound).
type WebhookInboundMeta struct {
	// PathPattern is the HTTP path the platform exposes for receiving webhook
	// callbacks from the external source (e.g. "/webhooks/stripe/events").
	PathPattern string `yaml:"pathPattern"`
	// SourceID is the secret-isolation key identifying the webhook source
	// (e.g. "stripe"). Must match the SourceID declared in every
	// webhook-receive ContractUsage for this contract. Must satisfy
	// ^[a-z][a-z0-9_-]{0,63}$ (aligns with runtime kernel/webhook
	// sourceIDPattern, maxLen 64).
	SourceID string `yaml:"sourceID"`
}

// SagaMeta is the orchestration block of a kind=saga contract.yaml. Steps run
// in slice order; each step's typed Run input is the previous step's output.
// The first step takes no typed input — the runtime feeds nil prevState. There
// is no saga-launch input payload in v1: the orchestrating cell loads the
// saga's initial business context by looking up domain state keyed by the
// enrolled Instance.ID (the correlation id).
type SagaMeta struct {
	// Timeout is the saga-wide deadline as a Go duration string (e.g. "30s").
	// Empty means no saga-level ceiling (per-step timeouts still apply).
	Timeout string `yaml:"timeout,omitempty"`
	// CompensationOrder selects the rollback walk. v1 limitation: only "reverse"
	// is supported (empty is treated as "reverse"); any other value is rejected
	// by governance SAGA-CONTRACT-COMPENSATION-ORDER-01. Forward/custom orders
	// are not yet modeled.
	CompensationOrder string `yaml:"compensationOrder,omitempty"`
	// Retries is the saga-wide default retry policy; each step inherits it
	// unless the step sets its own.
	Retries *SagaRetryMeta `yaml:"retries,omitempty"`
	// Steps lists the forward steps in execution order; must be non-empty.
	Steps []SagaStepMeta `yaml:"steps"`
}

// SagaStepMeta is one step inside a SagaMeta. Name must be a valid idutil.SafeID
// and unique within the saga. Output references a JSON Schema (contract-relative)
// describing this step's success payload — which becomes the next step's typed
// input. Compensate defaults to true (the step is rolled back during the
// Compensating phase); set false for terminal / non-reversible steps.
type SagaStepMeta struct {
	Name       string         `yaml:"name"`
	Output     string         `yaml:"output"`
	Timeout    string         `yaml:"timeout,omitempty"`
	Retries    *SagaRetryMeta `yaml:"retries,omitempty"`
	Compensate *bool          `yaml:"compensate,omitempty"`
}

// SagaRetryMeta is the declarative form of kernel/saga.RetryPolicy. Intervals
// are Go duration strings (e.g. "100ms"). Zero / empty fields inherit.
type SagaRetryMeta struct {
	MaxAttempts  int    `yaml:"maxAttempts,omitempty"`
	BaseInterval string `yaml:"baseInterval,omitempty"`
	MaxInterval  string `yaml:"maxInterval,omitempty"`
}

// JourneyMeta maps to journeys/J-*.yaml.
type JourneyMeta struct {
	ID           string          `yaml:"id"`
	Goal         string          `yaml:"goal"`
	Lifecycle    string          `yaml:"lifecycle"` // "active"|"experimental"
	Owner        OwnerMeta       `yaml:"owner"`
	Cells        []string        `yaml:"cells"`
	Contracts    []string        `yaml:"contracts"`
	PassCriteria []PassCriterion `yaml:"passCriteria"`
	File         string          `yaml:"-"` // parsed journey YAML path relative to project root
}

// PassCriterion is a single acceptance criterion within a Journey.
type PassCriterion struct {
	Text     string `yaml:"text"`
	Mode     string `yaml:"mode"` // "auto"|"manual"
	CheckRef string `yaml:"checkRef,omitempty"`
}

// AssemblyCellRef is one entry in assembly.yaml `cells:`. It accepts two YAML
// forms (see UnmarshalYAML):
//
//   - scalar shorthand       `- configcore`                          → {ID: "configcore"}
//   - object (cross-module)  `- {id: payment, module: github.com/acme/pay}`
//
// Module empty = the cell's cellmodules/ package lives in the assembly's own Go
// module (the common same-module case). A non-empty Module references a cell
// developed in an independent Go module (#1086); codegen renders the import as
// "<Module>/cellmodules/<ID>" instead of "<current module>/cellmodules/<ID>".
// The module must be a declared dependency (go.mod require / go.work use) — that
// is enforced by the Go compiler when the generated modules_gen.go is built, not
// by a metadata governance rule (a declared-but-unresolvable module fails
// `go build`).
//
// A non-empty Module also requires the owning assembly to declare
// `build.compositionAPI: true`; otherwise `gocell generate assembly` fails with
// ErrMetadataInvalid (the legacy local-CellModule form has no import path and
// cannot express a foreign module).
type AssemblyCellRef struct {
	// yaml tags drive only marshaling (decode is the custom UnmarshalYAML below);
	// module is omitempty so a round-trip of a same-module ref emits `id: x`
	// rather than `module: ""` (which the decoder rejects as explicit-empty).
	ID     string `yaml:"id"`
	Module string `yaml:"module,omitempty"`
}

// UnmarshalYAML decodes the scalar-or-object union form of an assembly cell
// entry. The decoder's KnownFields(true) strictness does not propagate into a
// custom Unmarshaler, so the mapping branch enforces the {id, module} key set
// itself to preserve strict-decode parity with the rest of the schema.
func (r *AssemblyCellRef) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		r.ID, r.Module = node.Value, ""
		return nil
	case yaml.MappingNode:
		return r.decodeMapping(node)
	default:
		return fmt.Errorf("line %d: assembly cell entry must be a string or {id, module} mapping", node.Line)
	}
}

// decodeMapping handles the object form `{id, module}` of an assembly cell
// entry, enforcing the known-field set (id, module), the required non-empty id,
// and module-path hygiene (a non-empty module must not carry characters that
// could be smuggled into the generated cellmodules import — defense-in-depth on
// top of the %q-quoting + Go-compiler gate; see ADR §D3).
func (r *AssemblyCellRef) decodeMapping(node *yaml.Node) error {
	var id, module string
	var sawID, sawModule bool
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode, valNode := node.Content[i], node.Content[i+1]
		switch keyNode.Value {
		case "id":
			if err := valNode.Decode(&id); err != nil {
				return err
			}
			sawID = true
		case "module":
			if err := valNode.Decode(&module); err != nil {
				return err
			}
			sawModule = true
		default:
			return fmt.Errorf("line %d: unknown field %q in assembly cell entry (allowed: id, module)",
				keyNode.Line, keyNode.Value)
		}
	}
	if !sawID || id == "" {
		return fmt.Errorf("line %d: assembly cell entry missing required field \"id\"", node.Line)
	}
	if err := validateOptionalModulePath(sawModule, module); err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	r.ID, r.Module = id, module
	return nil
}

// validateOptionalModulePath rejects an explicit-but-empty module (meaningless
// noise — omit it for same-module) and any module carrying control characters,
// whitespace, or quote/backslash runes that could break out of the generated
// import-path string literal.
func validateOptionalModulePath(present bool, module string) error {
	if !present {
		return nil
	}
	if module == "" {
		return fmt.Errorf("assembly cell entry \"module\" must be non-empty when set (omit it for same-module)")
	}
	// Single source: the char blocklist lives in metadata.AssemblyModulePathPattern
	// (byte-locked to the JSON schema). MatchAssemblyModulePath rejects the same
	// runes the prior imperative loop did — control+space range, DEL, quote,
	// backtick, backslash — that could break out of the generated cellmodules
	// import string literal.
	if !MatchAssemblyModulePath(module) {
		return fmt.Errorf("invalid character in assembly cell module %q", module)
	}
	return nil
}

// CellRefs builds a same-module []AssemblyCellRef from bare cell ids. It is the
// ergonomic constructor for the common case (every cell in the assembly's own
// module); cross-module entries are constructed as AssemblyCellRef{ID, Module}
// literals.
//
// This function body is the single sanctioned site that embeds an
// AssemblyCellRef{ID: <runtime value>} composite literal; it is allowlisted by
// FIXTURE-CELLID-TYPED-BUILDER-01/A1 (this file). A1 does not scan CellRefs
// call arguments. Usage guidance by A1 scope (kernel/):
//   - external kernel/ test packages with static ids → prefer explicit
//     []AssemblyCellRef{{ID: metadatatest.CellID*}} literals (A1-checked);
//   - internal `package metadata` tests (cannot import metadatatest — import
//     cycle) and dynamic-id helpers (ids are runtime params, not literals) →
//     use CellRefs (the only A1-compatible path);
//   - non-kernel ergonomics + production synthesis from validated runtime ids.
func CellRefs(ids ...string) []AssemblyCellRef {
	refs := make([]AssemblyCellRef, len(ids))
	for i, id := range ids {
		refs[i] = AssemblyCellRef{ID: id}
	}
	return refs
}

// CellIDs projects an []AssemblyCellRef to the bare cell-id slice, dropping
// module attribution. Used where only identity matters (entrypoint cell-id
// list, boundary cell set, devtools catalog wire shape).
func CellIDs(refs []AssemblyCellRef) []string {
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	return ids
}

// AssemblyMeta maps to assemblies/{id}/assembly.yaml (platform assemblies)
// or examples/{id}/assembly.yaml (example assemblies).
// See parser.matchAssemblyYAML for the recognized path forms.
type AssemblyMeta struct {
	ID    string            `yaml:"id"`
	Cells []AssemblyCellRef `yaml:"cells"`
	Owner OwnerMeta         `yaml:"owner"`
	// The assembly's provisioned capability set is NOT declared here — it is the
	// derived union of its cells' CellMeta.Requires (Design Y, #855). There is no
	// hand-authored assembly-level `capabilities` field; the single source is
	// per-cell. kernel/assembly.GenerateModulesGen computes the union when
	// rendering generatedCapabilities() in modules_gen.go.
	Build               BuildMeta `yaml:"build,omitempty"`
	MaxConsistencyLevel string    `yaml:"-"` // derived; yaml occurrence rejected by KnownFields
	Dir                 string    `yaml:"-"` // assembly directory name (parts[1]); set by parser from path, not YAML
	File                string    `yaml:"-"` // parsed assembly.yaml path relative to project root
}

// BuildMeta holds the build configuration for an Assembly.
type BuildMeta struct {
	Entrypoint     string `yaml:"entrypoint,omitempty"`
	Binary         string `yaml:"binary,omitempty"`
	DeployTemplate string `yaml:"deployTemplate,omitempty"`
	// CompositionAPI when true instructs GenerateModulesGen to emit the
	// runtime/composition.CellModule form (cellmodules/ module functions) instead
	// of the legacy local-CellModule-type form used by examples/.
	// Set to true in assemblies that use cellmodules/{cell}.Module() constructors
	// (currently only assemblies/corebundle/assembly.yaml).
	CompositionAPI bool `yaml:"compositionAPI,omitempty"`
}

// StatusBoardEntry maps to a single entry in journeys/status-board.yaml.
type StatusBoardEntry struct {
	JourneyID string `json:"journeyId" yaml:"journeyId"`
	State     string `json:"state"     yaml:"state"`
	Risk      string `json:"risk"      yaml:"risk"`
	Blocker   string `json:"blocker"   yaml:"blocker"`
	UpdatedAt string `json:"updatedAt" yaml:"updatedAt"`
}

// ActorMeta maps to a single entry in actors.yaml.
//
// actors.yaml registers external systems that participate in contracts but
// are not modeled as cells. Membership in this file is itself the type
// declaration ("registered actor = external"); audience-aware governance
// rules (REF-17) reject any actor in this file from internal endpoints.
// No type field is needed — keeping it would create a parallel truth source
// against the cell-vs-actor distinction already implied by membership.
type ActorMeta struct {
	ID                  string `yaml:"id"`
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
