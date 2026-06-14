package cellvocab

import (
	"fmt"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

const internalValueQuotedFmt = "value=%q"

// InternalPathPrefix is the URL prefix that designates an internal-listener route.
// Shared by kernel/cell.AuthRouteMeta.IsInternal and kernel/contractspec.Validate.
const InternalPathPrefix = "/internal/v1/"

// AdminPathPrefix is the URL prefix that designates an admin-listener
// (operator control-plane) route. Shared by kernel/cell.AuthRouteMeta.IsAdmin
// and the runtime router's listener-route affinity check.
const AdminPathPrefix = "/admin/v1/"

// CellType classifies a Cell's architectural role.
type CellType string

const (
	CellTypeCore    CellType = "core"
	CellTypeEdge    CellType = "edge"
	CellTypeSupport CellType = "support"
)

// ContractKind classifies the communication pattern of a Contract.
type ContractKind string

const (
	ContractHTTP       ContractKind = "http"
	ContractEvent      ContractKind = "event"
	ContractCommand    ContractKind = "command"
	ContractProjection ContractKind = "projection"
	ContractWebhook    ContractKind = "webhook"
	ContractGRPC       ContractKind = "grpc"
	ContractSaga       ContractKind = "saga"
)

// ContractRole describes how a Slice participates in a Contract.
type ContractRole string

const (
	RoleServe     ContractRole = "serve"
	RoleCall      ContractRole = "call"
	RolePublish   ContractRole = "publish"
	RoleSubscribe ContractRole = "subscribe"
	RoleHandle    ContractRole = "handle"
	RoleInvoke    ContractRole = "invoke"
	RoleProvide   ContractRole = "provide"
	RoleRead      ContractRole = "read"
	// RoleOrchestrate is the saga provider role: the cell that owns and drives
	// the saga definition (declares the contract via endpoints.server). Saga has
	// no consumer-side role — participation is one orchestrating cell per saga.
	RoleOrchestrate ContractRole = "orchestrate"
	// Webhook roles: a cell either receives inbound webhooks (consumer-side) or
	// dispatches outbound webhooks (provider-side). See ValidRolesForKind.
	RoleWebhookReceive  ContractRole = "webhook-receive"
	RoleWebhookDispatch ContractRole = "webhook-dispatch"
)

// Transport names the wire protocol a contract binds to. A contract declares a
// non-empty SET of sanctioned transports via contract.yaml `transports:`
// (defaulted per-kind by the parser when omitted); contractgen derives the
// generated ContractSpec.Transport as the primary (transports[0]). This typed
// const set is the SINGLE SOURCE for the contract.schema.json transports enum
// (byte-locked by TestSchemaConstantsMatchSchemaLiterals#transportEnum),
// governance FMT-39, and runtime metadata.IsKnownTransport. Unlike AsyncAPI's
// open `protocol` string, GoCell's transport set is closed: a platform-owned
// kernel fails fast on an unknown transport rather than discovering it at
// runtime (mirrors k8s core/v1 Protocol +enum).
// ref: kubernetes/api core/v1/types.go (Protocol +enum); asyncapi/spec server.protocol.
type Transport string

const (
	TransportAMQP     Transport = "amqp"
	TransportMQTT     Transport = "mqtt"
	TransportInternal Transport = "internal"
	TransportHTTP     Transport = "http"
	TransportGRPC     Transport = "grpc"
)

// WebhookDirection is the flow direction of a kind=webhook contract: inbound
// (external → cell, receiver-side) or outbound (cell → external, dispatcher-side).
// Single source shared by kernel/metadata (parser webhook derivation) and
// kernel/governance (FMT-38), so the direction string never drifts across layers.
type WebhookDirection string

const (
	DirectionInbound  WebhookDirection = "inbound"
	DirectionOutbound WebhookDirection = "outbound"
)

// allContractKinds is the canonical ordered set of ContractKind values. It is
// the SINGLE SOURCE for both the contract.schema.json `kind` enum (byte-locked
// by TestSchemaConstantsMatchSchemaLiterals) and the governance validKinds set.
// Order matches the schema enum; do not reorder without updating the schema in
// lockstep. ParseContractKind round-trips every member (asserted in the
// cellvocab round-trip test), so this slice, the typed consts, the parser, the
// schema literal, and governance cannot drift apart.
var allContractKinds = []ContractKind{
	ContractHTTP, ContractEvent, ContractCommand, ContractProjection, ContractWebhook, ContractGRPC, ContractSaga,
}

// allContractRoles is the canonical ordered set of ContractRole values. Single
// source for the slice.schema.json contractUsages `role` enum (byte-locked by
// TestSchemaConstantsMatchSchemaLiterals) and the governance validRoles set.
// Order matches the schema enum; do not reorder without updating the schema.
var allContractRoles = []ContractRole{
	RoleServe, RoleCall, RolePublish, RoleSubscribe, RoleHandle,
	RoleInvoke, RoleProvide, RoleRead, RoleOrchestrate,
	RoleWebhookReceive, RoleWebhookDispatch,
}

// AllContractKinds returns a copy of the canonical ordered ContractKind set.
// Callers that need string values convert with string(k). The schema enum and
// governance validKinds derive from this slice so the accepted kind set has one
// source of truth.
func AllContractKinds() []ContractKind {
	out := make([]ContractKind, len(allContractKinds))
	copy(out, allContractKinds)
	return out
}

// AllContractRoles returns a copy of the canonical ordered ContractRole set.
// The schema role enum and governance validRoles derive from this slice.
func AllContractRoles() []ContractRole {
	out := make([]ContractRole, len(allContractRoles))
	copy(out, allContractRoles)
	return out
}

// allTransports is the canonical ordered set of Transport values. SINGLE SOURCE
// for the contract.schema.json transports enum (byte-locked by
// TestSchemaConstantsMatchSchemaLiterals#transportEnum), metadata.TransportEnum
// (governance FMT-39 + runtime IsKnownTransport), and the per-kind default
// derivation in the parser. Order matches the schema enum; do not reorder
// without updating the schema in lockstep. ParseTransport round-trips every
// member (asserted in the cellvocab round-trip test).
var allTransports = []Transport{
	TransportAMQP, TransportMQTT, TransportInternal, TransportHTTP, TransportGRPC,
}

// AllTransports returns a copy of the canonical ordered Transport set. The
// schema transports enum and metadata.TransportEnum derive from this slice so
// the accepted transport set has one source of truth.
func AllTransports() []Transport {
	out := make([]Transport, len(allTransports))
	copy(out, allTransports)
	return out
}

// ContractLifecycle represents the wire-stability governance state of a Contract
// (draft / active / deprecated). This is orthogonal to CellLifecycle (the
// cell/slice maturity axis) and JourneyMeta.Lifecycle (journey delivery status).
type ContractLifecycle string

const (
	ContractLifecycleDraft      ContractLifecycle = "draft"
	ContractLifecycleActive     ContractLifecycle = "active"
	ContractLifecycleDeprecated ContractLifecycle = "deprecated"
)

// ParseCellType parses a string into a CellType.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseCellType(s string) (CellType, error) {
	switch s {
	case "core":
		return CellTypeCore, nil
	case "edge":
		return CellTypeEdge, nil
	case "support":
		return CellTypeSupport, nil
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid cell type",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalValueQuotedFmt, s))))
	}
}

// ParseContractKind parses a string into a ContractKind.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseContractKind(s string) (ContractKind, error) {
	switch s {
	case "http":
		return ContractHTTP, nil
	case "event":
		return ContractEvent, nil
	case "command":
		return ContractCommand, nil
	case "projection":
		return ContractProjection, nil
	case "webhook":
		return ContractWebhook, nil
	case "grpc":
		return ContractGRPC, nil
	case "saga":
		return ContractSaga, nil
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid contract kind",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalValueQuotedFmt, s))))
	}
}

// ParseContractRole parses a string into a ContractRole.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseContractRole(s string) (ContractRole, error) {
	switch s {
	case "serve":
		return RoleServe, nil
	case "call":
		return RoleCall, nil
	case "publish":
		return RolePublish, nil
	case "subscribe":
		return RoleSubscribe, nil
	case "handle":
		return RoleHandle, nil
	case "invoke":
		return RoleInvoke, nil
	case "provide":
		return RoleProvide, nil
	case "read":
		return RoleRead, nil
	case "orchestrate":
		return RoleOrchestrate, nil
	case "webhook-receive":
		return RoleWebhookReceive, nil
	case "webhook-dispatch":
		return RoleWebhookDispatch, nil
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid contract role",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalValueQuotedFmt, s))))
	}
}

// ParseTransport parses a string into a Transport.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseTransport(s string) (Transport, error) {
	switch s {
	case "amqp":
		return TransportAMQP, nil
	case "mqtt":
		return TransportMQTT, nil
	case "internal":
		return TransportInternal, nil
	case "http":
		return TransportHTTP, nil
	case "grpc":
		return TransportGRPC, nil
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid transport",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalValueQuotedFmt, s))))
	}
}

// ParseContractLifecycle parses a string into a ContractLifecycle.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseContractLifecycle(s string) (ContractLifecycle, error) {
	switch s {
	case "draft":
		return ContractLifecycleDraft, nil
	case "active":
		return ContractLifecycleActive, nil
	case "deprecated":
		return ContractLifecycleDeprecated, nil
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid contract lifecycle",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalValueQuotedFmt, s))))
	}
}
