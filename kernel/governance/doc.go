// Package governance implements validation rules for GoCell metadata.
// It checks referential integrity, topological legality, verify closure,
// format compliance, and advisory warnings across the parsed ProjectMeta.
//
// governance is invoked by `gocell validate` (cmd/gocell/app) and by CI;
// it is a pure validation tool — no runtime / adapter side effects, no
// goroutine ownership, no I/O beyond reading filesystem-resident YAML
// during the parse phase.
//
// Rule numbering: see kernel/governance/CLAUDE.md for the REF / TOPO /
// VERIFY / FMT / ADV / OUTGARD series and the ValidationResult schema.
package governance
