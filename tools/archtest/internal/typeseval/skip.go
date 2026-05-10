package typeseval

import "strings"

// IsGeneratedRelPath reports whether rel points to codegen output under the
// repo's generated/ tree. Single source for archtest rules that load
// packages via SharedResolver / packages.Load and must skip generated code
// (HandleResult literals, ContractSpec literals, etc.).
//
// Definition: rel begins with "generated/" (top-level only). The repo
// reserves exactly one generated/ directory at module root; sub-tree
// "generated/" inside a hand-written package would be a layout violation
// and is intentionally not matched.
//
// Why not handled by go list defaults: `go list ./...` does include
// generated/contracts/.../v1 packages — the comment in
// outbox_invariants_test.go (above TestOutboxHandleResultFactoryPreferred)
// previously claimed the opposite, which was the original PR445-FU
// finding F4. Rules using SharedResolver with the "./..." pattern MUST
// therefore filter rel paths through this helper before scanning.
//
// Rules that walk files via scanner.ModuleScope / scanner.DirsScope
// already exclude generated/ at the file-walk layer (those scopes have
// generated/ in their default skip set) and do not need this helper.
//
// Closes PR445-FU finding F4. Tracking future cross-rule invariant:
// backlog item GENERATED-SKIP-CROSS-RULE-INVARIANT-01.
func IsGeneratedRelPath(rel string) bool {
	return strings.HasPrefix(rel, "generated/")
}
