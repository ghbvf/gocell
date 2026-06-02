package contractgen

import "fmt"

// protoRegistry detects proto-service collisions across the grpc contracts in
// one generate run and pins each proto service to a single import path. It is
// the codegen-time enforcement of data-model.md's "(package, service, method)
// globally unique" rule and a carrier of GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01
// (C4). The collision logic is unit-tested directly; the production pre-pass
// (checkGRPCProtoCollisions) iterates the empty grpc-contract set until the
// first real grpc contract lands (PR 8), at which point it becomes live without
// any wiring change.
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
	methods    map[string]struct{}
}

func newProtoRegistry() *protoRegistry {
	return &protoRegistry{entries: make(map[protoServiceKey]*protoServiceEntry)}
}

// register records one grpc contract's (proto package, service, method) and its
// resolved import path, failing fast on two collision classes:
//   - the same (package, service) mapped to divergent import paths;
//   - a duplicate (package, service, method) across contracts.
func (r *protoRegistry) register(contractID, service, method string, info protoTypeInfo) error {
	key := protoServiceKey{pkg: info.ProtoPackage, service: service}
	e, ok := r.entries[key]
	if !ok {
		r.entries[key] = &protoServiceEntry{
			importPath: info.ImportPath,
			methods:    map[string]struct{}{method: {}},
		}
		return nil
	}
	if e.importPath != info.ImportPath {
		return fmt.Errorf(
			"contractgen: grpc proto service %q (package %q) maps to divergent import paths %q and %q (contract %q)",
			service, info.ProtoPackage, e.importPath, info.ImportPath, contractID)
	}
	if _, dup := e.methods[method]; dup {
		return fmt.Errorf(
			"contractgen: grpc (package %q, service %q, method %q) already registered; duplicate in contract %q",
			info.ProtoPackage, service, method, contractID)
	}
	e.methods[method] = struct{}{}
	return nil
}
