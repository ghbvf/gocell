// Package cell defines the core types and interfaces for the GoCell Cell model.
package cell

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// CellType classifies a Cell's architectural role.
type CellType string

const (
	CellTypeCore    CellType = "core"
	CellTypeEdge    CellType = "edge"
	CellTypeSupport CellType = "support"
)

// Level represents the consistency level (L0-L4) of a Cell or Contract.
type Level int

const (
	L0 Level = iota // LocalOnly
	L1              // LocalTx
	L2              // OutboxFact
	L3              // WorkflowEventual
	L4              // DeviceLatent
)

// levelStrings maps Level values to their string representations.
var levelStrings = [...]string{"L0", "L1", "L2", "L3", "L4"}

// String returns the string representation of a Level (e.g. "L0", "L2").
func (l Level) String() string {
	if l >= L0 && int(l) < len(levelStrings) {
		return levelStrings[l]
	}
	return fmt.Sprintf("Level(%d)", int(l))
}

// ParseLevel parses a string like "L0" or "L3" into a Level.
// Returns errcode.ErrValidationFailed for unrecognised input.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "L0":
		return L0, nil
	case "L1":
		return L1, nil
	case "L2":
		return L2, nil
	case "L3":
		return L3, nil
	case "L4":
		return L4, nil
	default:
		return 0, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("invalid consistency level: %q", s))
	}
}

// HealthStatus reports the health of a Cell.
type HealthStatus struct {
	Status  string            // "healthy" | "degraded" | "unhealthy"
	Details map[string]string
}

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
