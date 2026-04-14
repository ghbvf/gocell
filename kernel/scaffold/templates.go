// Package scaffold generates directory structures and YAML metadata files
// for GoCell cells, slices, contracts, and journeys.
//
// Design decisions (ref: go-zero goctl):
//   - embed templates: .tpl files are embedded via //go:embed
//   - skip-on-conflict: existing files are never overwritten
//   - genFile abstraction: unified template render + write function
//   - strong-typed context: struct opts replace map[string]any (divergence from goctl)
package scaffold

import "embed"

//go:embed templates/*.tpl
var templateFS embed.FS
