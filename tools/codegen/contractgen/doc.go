// Package contractgen renders contract DTOs / Service interfaces / HTTP handlers
// from contract.yaml + JSON Schema schemaRefs.
//
// Reference:
//
//	ref: oapi-codegen pkg/codegen/codegen.go@master
//	ref: go-zero tools/goctl/api/gogen/gentypes.go@master
//	ref: sqlc-gen-go internal/gen.go@main
//
// Generated artifacts live under generated/contracts/<kind>/<domain-path>/v<N>/:
//   - types_gen.go    — Request / Response / Payload structs (always)
//   - iface_gen.go    — Service (HTTP) / Handler (event) interface (always)
//   - handler_gen.go  — HTTP adapter (kind=http only; not for event)
//
// User code implements the generated interface. The generated handler wires
// HTTP decode/encode + auth.Mount with a user-provided auth.Policy.
package contractgen
