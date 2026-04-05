// Package slice provides slice-level runtime support for the GoCell kernel.
//
// A Slice is a cohesive sub-unit within a Cell. This package provides:
//
//   - Slice registration and lifecycle management
//   - File ownership enforcement (allowedFiles convention)
//   - Contract usage validation (contractUsages in slice.yaml)
//   - Verification spec management (unit tests, contract tests, waivers)
//
// Each slice belongs to exactly one cell and inherits the cell's owner and
// consistency level unless explicitly overridden.
package slice
