// Package verify provides verify-command introspection used by
// `gocell verify` and by kernel/governance VERIFY-* rules.
//
// A "verify command" is a shell command declared in cell.yaml /
// slice.yaml verify.{smoke,unit,contract} fields; verify reads those
// declarations from a parsed ProjectMeta and reports completeness,
// uniqueness, and reachability.
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
// kernel/verify imports only kernel/metadata. It is consumed by
// kernel/governance and by `gocell verify`; nothing in kernel/ imports
// back into kernel/verify.
//
// verify does NOT execute commands — it only inspects the declared
// strings. Actual execution is `gocell run-journey` and CI's
// `hack/make-rules/verify.sh`.
package verify
