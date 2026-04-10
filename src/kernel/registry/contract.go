// Package registry provides indexed, read-only access to parsed GoCell
// project metadata (cells, slices, contracts).
package registry

import (
	"sort"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// ContractRegistry provides indexed access to contracts.
type ContractRegistry struct {
	contracts map[string]*metadata.ContractMeta
	byKind    map[string][]*metadata.ContractMeta  // keyed by kind string
	byOwner   map[string][]*metadata.ContractMeta  // keyed by ownerCell
}

// NewContractRegistry builds a registry from parsed project metadata.
func NewContractRegistry(project *metadata.ProjectMeta) *ContractRegistry {
	r := &ContractRegistry{
		contracts: make(map[string]*metadata.ContractMeta),
		byKind:    make(map[string][]*metadata.ContractMeta),
		byOwner:   make(map[string][]*metadata.ContractMeta),
	}
	if project == nil {
		return r
	}
	for id, c := range project.Contracts {
		if c == nil {
			continue
		}
		r.contracts[id] = c
		r.byKind[c.Kind] = append(r.byKind[c.Kind], c)
		r.byOwner[c.OwnerCell] = append(r.byOwner[c.OwnerCell], c)
	}
	return r
}

// Get returns a shallow copy of a contract by ID, or nil if not found.
func (r *ContractRegistry) Get(id string) *metadata.ContractMeta {
	c := r.contracts[id]
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// ByKind returns copies of all contracts of the given kind.
func (r *ContractRegistry) ByKind(kind string) []*metadata.ContractMeta {
	return copyContractSlice(r.byKind[kind])
}

// ByOwner returns copies of all contracts owned by the given cell.
func (r *ContractRegistry) ByOwner(cellID string) []*metadata.ContractMeta {
	return copyContractSlice(r.byOwner[cellID])
}

func copyContractSlice(src []*metadata.ContractMeta) []*metadata.ContractMeta {
	if len(src) == 0 {
		return nil
	}
	out := make([]*metadata.ContractMeta, len(src))
	for i, c := range src {
		cp := *c
		out[i] = &cp
	}
	return out
}

// Provider returns the provider actor ID for a contract.
// For http: server, event: publisher, command: handler, projection: provider.
// Returns empty string if the contract is not found or kind is unknown.
func (r *ContractRegistry) Provider(contractID string) string {
	c := r.contracts[contractID]
	if c == nil {
		return ""
	}
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

// Consumers returns the consumer actor IDs for a contract.
// For http: clients, event: subscribers, command: invokers, projection: readers.
// Returns nil if the contract is not found or kind is unknown.
func (r *ContractRegistry) Consumers(contractID string) []string {
	c := r.contracts[contractID]
	if c == nil {
		return nil
	}
	switch c.Kind {
	case "http":
		return append([]string(nil), c.Endpoints.Clients...)
	case "event":
		return append([]string(nil), c.Endpoints.Subscribers...)
	case "command":
		return append([]string(nil), c.Endpoints.Invokers...)
	case "projection":
		return append([]string(nil), c.Endpoints.Readers...)
	default:
		return nil
	}
}

// AllIDs returns all contract IDs sorted alphabetically.
func (r *ContractRegistry) AllIDs() []string {
	ids := make([]string, 0, len(r.contracts))
	for id := range r.contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Count returns the total number of contracts.
func (r *ContractRegistry) Count() int {
	return len(r.contracts)
}
