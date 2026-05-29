package cellvocab

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const internalValueQuotedFmt = "value=%q"

// InternalPathPrefix is the URL prefix that designates an internal-listener route.
// Shared by kernel/cell.AuthRouteMeta.IsInternal and kernel/contractspec.Validate.
const InternalPathPrefix = "/internal/v1/"

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
)

// allContractKinds is the canonical ordered set of ContractKind values. It is
// the SINGLE SOURCE for both the contract.schema.json `kind` enum (byte-locked
// by TestSchemaConstantsMatchSchemaLiterals) and the governance validKinds set.
// Order matches the schema enum; do not reorder without updating the schema in
// lockstep. ParseContractKind round-trips every member (asserted in the
// cellvocab round-trip test), so this slice, the typed consts, the parser, the
// schema literal, and governance cannot drift apart.
var allContractKinds = []ContractKind{
	ContractHTTP, ContractEvent, ContractCommand, ContractProjection, ContractGRPC, ContractSaga,
}

// allContractRoles is the canonical ordered set of ContractRole values. Single
// source for the slice.schema.json contractUsages `role` enum (byte-locked by
// TestSchemaConstantsMatchSchemaLiterals) and the governance validRoles set.
// Order matches the schema enum; do not reorder without updating the schema.
var allContractRoles = []ContractRole{
	RoleServe, RoleCall, RolePublish, RoleSubscribe, RoleHandle,
	RoleInvoke, RoleProvide, RoleRead, RoleOrchestrate,
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
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid contract role",
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
