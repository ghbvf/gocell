// Package registry provides the cell and contract registry for the GoCell
// kernel.
//
// The registry maintains a runtime index of all registered cells, slices,
// and contracts. It supports:
//
//   - Cell lookup by ID
//   - Contract lookup by ID and kind
//   - Dependency graph traversal
//   - Duplicate detection and conflict resolution
//
// The registry is populated during assembly startup (CoreAssembly.Start)
// and is read-only after initialization.
package registry
