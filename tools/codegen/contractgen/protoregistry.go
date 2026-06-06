package contractgen

import "fmt"

// protoRegistry detects proto-service collisions across the grpc contracts in
// one generate run and pins each proto service to a single import path. It is
// the codegen-time enforcement of data-model.md's "(package, service) globally
// unique" rule and a carrier of GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01 (C4).
//
// With service-level contract granularity (#1655) the ownership unit is a whole
// proto service (not an individual rpc), so the only collision class that
// matters is two different contracts claiming the same (proto package, service).
// Per-method collision tracking is no longer needed — a service's method set is
// fully determined by the .proto file, and a single contract owns the service.
type protoRegistry struct {
	entries map[protoServiceKey]*protoServiceEntry
}

// protoServiceKey identifies a proto service by its proto package + FQ service
// name — the natural collision key across contracts.
type protoServiceKey struct {
	pkg     string
	service string
}

type protoServiceEntry struct {
	importPath string
	contractID string // first contract that registered this service (for diagnostics)
}

func newProtoRegistry() *protoRegistry {
	return &protoRegistry{entries: make(map[protoServiceKey]*protoServiceEntry)}
}

// register records one grpc contract's (proto package, service) and its
// resolved import path, failing fast on two collision classes:
//   - the same (package, service) mapped to divergent import paths;
//   - a duplicate (package, service) across contracts (two contracts claiming
//     ownership of the same proto service).
func (r *protoRegistry) register(contractID, service string, info ProtoServiceInfo) error {
	key := protoServiceKey{pkg: info.ProtoPackage, service: service}
	e, ok := r.entries[key]
	if !ok {
		r.entries[key] = &protoServiceEntry{
			importPath: info.ImportPath,
			contractID: contractID,
		}
		return nil
	}
	if e.importPath != info.ImportPath {
		return fmt.Errorf(
			"contractgen: grpc proto service %q (package %q) maps to divergent import paths %q and %q (contract %q)",
			service, info.ProtoPackage, e.importPath, info.ImportPath, contractID)
	}
	return fmt.Errorf(
		"contractgen: grpc service %q (package %q) already registered by contract %q; duplicate ownership in contract %q",
		service, info.ProtoPackage, e.contractID, contractID)
}
