// Package journey provides query access to Journey metadata and status,
// including catalog loading and status-board parsing. Journeys are the
// acceptance specifications declared in journeys/J-*.yaml plus the
// dynamic delivery state in journeys/status-board.yaml.
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
// kernel/journey imports only kernel/metadata (for ProjectMeta access).
// It is consumed by `gocell` CLI commands and by runtime/devtools/catalog;
// nothing in kernel/ imports back into kernel/journey.
//
// Catalog loading is read-only: journey YAML is parsed once at startup
// and cached. The status-board file is consulted by introspection tools
// but is not authoritative — the actual delivery state lives in CI.
package journey
