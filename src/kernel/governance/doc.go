// Package governance provides governance and compliance tools for the GoCell
// kernel.
//
// It enforces architectural rules such as:
//
//   - Dependency direction (kernel -> pkg only; cells -> kernel + runtime only)
//   - Prohibited field names (legacy metadata fields)
//   - Cell isolation (no direct imports between cells)
//   - Consistency level constraints (L0-L4)
//
// Governance checks can be run as part of CI/CD or via the gocell CLI.
package governance
