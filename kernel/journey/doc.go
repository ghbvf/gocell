// Package journey provides query access to Journey metadata and status,
// including catalog loading and status-board parsing. Journeys are the
// acceptance specifications declared in journeys/J-*.yaml plus the
// dynamic delivery state in journeys/status-board.yaml.
//
// Catalog loading is read-only: journey YAML is parsed once at startup
// and cached. The status-board file is consulted by introspection tools
// but is not authoritative — the actual delivery state lives in CI.
package journey
