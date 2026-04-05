package schemas

import "embed"

// FS embeds all JSON Schema files for metadata validation.
// Used by kernel/governance FMT rules for structural validation (planned Phase 2).
// Also available for IDE tooling via yaml-language-server $schema references.
//
//go:embed *.json
var FS embed.FS
