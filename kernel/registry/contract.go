// Package registry provides indexed, read-only access to parsed GoCell
// project metadata (cells, slices, contracts).
package registry

import (
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ContractRegistry provides indexed access to contracts.
type ContractRegistry struct {
	contracts map[string]*metadata.ContractMeta
	byKind    map[string][]*metadata.ContractMeta // keyed by kind string
	byOwner   map[string][]*metadata.ContractMeta // keyed by ownerCell
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

// Get returns a deep copy of a contract by ID, or nil if not found.
func (r *ContractRegistry) Get(id string) *metadata.ContractMeta {
	c := r.contracts[id]
	if c == nil {
		return nil
	}
	return deepCopyContract(c)
}

// ByKind returns deep copies of all contracts of the given kind.
func (r *ContractRegistry) ByKind(kind string) []*metadata.ContractMeta {
	return copyContractSlice(r.byKind[kind])
}

// ByOwner returns deep copies of all contracts owned by the given cell.
func (r *ContractRegistry) ByOwner(cellID string) []*metadata.ContractMeta {
	return copyContractSlice(r.byOwner[cellID])
}

func copyContractSlice(src []*metadata.ContractMeta) []*metadata.ContractMeta {
	if len(src) == 0 {
		return nil
	}
	out := make([]*metadata.ContractMeta, len(src))
	for i, c := range src {
		out[i] = deepCopyContract(c)
	}
	return out
}

func deepCopyContract(c *metadata.ContractMeta) *metadata.ContractMeta {
	cp := *c
	// Deep copy mutable Endpoints slices.
	cp.Endpoints.Clients = append([]string(nil), c.Endpoints.Clients...)
	cp.Endpoints.ActorSubscribers = append([]string(nil), c.Endpoints.ActorSubscribers...)
	cp.Endpoints.Subscribers = append([]string(nil), c.Endpoints.Subscribers...)
	cp.Endpoints.Invokers = append([]string(nil), c.Endpoints.Invokers...)
	cp.Endpoints.Readers = append([]string(nil), c.Endpoints.Readers...)
	// Deep copy derived webhook endpoint slices (yaml:"-", populated by
	// deriveWebhookEndpoints) so a copy cannot alias the registry's source.
	cp.Endpoints.Receivers = append([]string(nil), c.Endpoints.Receivers...)
	cp.Endpoints.Dispatchers = append([]string(nil), c.Endpoints.Dispatchers...)
	// Deep copy Replayable pointer.
	if c.Replayable != nil {
		v := *c.Replayable
		cp.Replayable = &v
	}
	// Deep copy webhook pointer fields so a copy cannot alias the source.
	if c.Endpoints.Inbound != nil {
		v := *c.Endpoints.Inbound
		cp.Endpoints.Inbound = &v
	}
	if c.Signature != nil {
		v := *c.Signature
		cp.Signature = &v
	}
	if c.Payload != nil {
		v := *c.Payload
		cp.Payload = &v
	}
	return &cp
}

// Provider returns the provider actor ID for a contract.
// For http: server, event: publisher, command: handler, projection: provider,
// webhook: ownerCell (matches metadata.ContractMeta.ProviderEndpoint).
// Returns an error if the contract is not found or the kind is unknown.
func (r *ContractRegistry) Provider(contractID string) (string, error) {
	c := r.contracts[contractID]
	if c == nil {
		return "", errcode.New(errcode.KindNotFound, errcode.ErrContractNotFound,
			"contract not found in registry",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q", contractID))))
	}
	switch c.Kind {
	case "http":
		return c.Endpoints.Server, nil
	case "event":
		return c.Endpoints.Publisher, nil
	case "command":
		return c.Endpoints.Handler, nil
	case "projection":
		return c.Endpoints.Provider, nil
	case "webhook":
		return c.OwnerCell, nil
	default:
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown contract kind",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q kind=%q", contractID, c.Kind))))
	}
}

// Consumers returns the consumer actor IDs for a contract.
// For http: clients, event: subscribers, command: invokers, projection: readers,
// webhook: receivers (the inbound consumer cells; matches governance
// contractConsumers/consumerFieldName). Dispatchers are outbound senders, not
// consumers, so they are not returned here.
// Returns an error if the contract is not found or the kind is unknown.
func (r *ContractRegistry) Consumers(contractID string) ([]string, error) {
	c := r.contracts[contractID]
	if c == nil {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrContractNotFound,
			"contract not found in registry",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q", contractID))))
	}
	switch c.Kind {
	case "http":
		return append([]string(nil), c.Endpoints.Clients...), nil
	case "event":
		return append([]string(nil), c.Endpoints.Subscribers...), nil
	case "command":
		return append([]string(nil), c.Endpoints.Invokers...), nil
	case "projection":
		return append([]string(nil), c.Endpoints.Readers...), nil
	case "webhook":
		return append([]string(nil), c.Endpoints.Receivers...), nil
	default:
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown contract kind",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q kind=%q", contractID, c.Kind))))
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
