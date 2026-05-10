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
)

// Lifecycle represents the governance state of a Contract.
type Lifecycle string

const (
	LifecycleDraft      Lifecycle = "draft"
	LifecycleActive     Lifecycle = "active"
	LifecycleDeprecated Lifecycle = "deprecated"
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
			errcode.WithInternal(fmt.Sprintf(internalValueQuotedFmt, s)))
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
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid contract kind",
			errcode.WithInternal(fmt.Sprintf(internalValueQuotedFmt, s)))
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
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid contract role",
			errcode.WithInternal(fmt.Sprintf(internalValueQuotedFmt, s)))
	}
}

// ParseLifecycle parses a string into a Lifecycle.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseLifecycle(s string) (Lifecycle, error) {
	switch s {
	case "draft":
		return LifecycleDraft, nil
	case "active":
		return LifecycleActive, nil
	case "deprecated":
		return LifecycleDeprecated, nil
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid lifecycle",
			errcode.WithInternal(fmt.Sprintf(internalValueQuotedFmt, s)))
	}
}
