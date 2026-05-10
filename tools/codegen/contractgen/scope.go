package contractgen

// Scope is a sealed interface that controls which contracts Generate processes.
// Use ScopeAll, ScopeContracts, or ScopeCell to construct a value.
// Passing nil as Scope to Generate will return an error.
//
// RED stub: Generate does not yet read Scope. GREEN phase will integrate it.
type Scope interface {
	contractScope()
}

// ScopeAll instructs Generate to process all contracts with Codegen=true.
// This is the current default behavior (equivalent to omitting OnlyContract).
type ScopeAll struct{}

func (ScopeAll) contractScope() {}

// ScopeContracts restricts Generate to the given list of contract IDs.
// Each ID must exist and have Codegen=true.
type ScopeContracts []string

func (ScopeContracts) contractScope() {}

// ScopeCell restricts Generate to contracts owned by (server/publisher) the
// given cell ID. Useful for per-cell codegen invocations.
type ScopeCell string

func (ScopeCell) contractScope() {}
