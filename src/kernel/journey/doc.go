// Package journey provides journey verification support for the GoCell kernel.
//
// A Journey (J-*.yaml) defines an end-to-end user workflow that spans
// multiple cells and slices. This package provides:
//
//   - Journey specification loading and validation
//   - Step-by-step execution tracing for integration tests
//   - Status board integration for delivery tracking
//
// Journey status (readiness, risk, blocker, done) is maintained exclusively
// in journeys/status-board.yaml and never in cell/slice/contract metadata.
package journey
