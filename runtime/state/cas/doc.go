// Package cas provides a typed-Go-heavy Protocol primitive for
// compare-and-swap (CAS) optimistic concurrency control.
//
// Conceptually, CAS is modeled after Kubernetes resourceVersion and etcd
// compare-then-put: a writer reads the current version, passes it as an
// expected version on the mutation, and the store rejects the write if the
// current version has advanced (i.e. a concurrent writer won).
//
// # Design contrast with session and ledger
//
// Unlike runtime/auth/session and runtime/audit/ledger, cas does NOT expose a
// Store interface, mem_store, or storetest suite. CAS is a write-time policy
// primitive, not an entity lifecycle: each cell owns its own private entities
// (User, ConfigEntry, FeatureFlag, …) and their schemas cannot be unified into
// a shared generic Store without leaking cell-private domain types across cell
// boundaries — a violation of the GoCell cross-cell isolation rule.
//
// Conformance is proved by cell-private repository race-condition integration
// tests (real DB with concurrent writers), not by a shared storetest suite.
//
// # What this package provides
//
//   - ConflictPolicy sealed interface — today only ConflictPolicyStrictReject
//     (HTTP 409, no retry). Future siblings (LastWriteWins, RetryWithMerge) may
//     be added here without breaking callers.
//   - Protocol value — bundles the versionField name and a ConflictPolicy. Constructed
//     via NewProtocol or MustNewProtocol (composition root only).
//   - CheckVersionMatch — translates UPDATE/DELETE RowsAffected into the standard
//     ErrVersionConflict error vocabulary so cell repos share a single check point.
//
// ref: docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md
package cas
