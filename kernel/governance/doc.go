// Package governance implements validation rules for GoCell metadata.
// It checks referential integrity, topological legality, verify closure,
// format compliance, and advisory warnings across the parsed ProjectMeta.
//
// governance is invoked by `gocell validate` (cmd/gocell/app) and by CI;
// it is a pure validation tool — no runtime / adapter side effects, no
// goroutine ownership, no I/O beyond reading filesystem-resident YAML
// during the parse phase.
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
// kernel/governance imports kernel/cellvocab (for ContractKind /
// ContractRole / Lifecycle / CellType vocabulary), kernel/clock (for
// timestamps in advisory checks), kernel/metadata (the parsed project
// model), kernel/registry (cell/contract registries used by some rules),
// and kernel/verify (verify-command introspection). It does NOT import
// kernel/cell — after the G-04 refactor, the runtime cell package is no
// longer reachable from a validation tool. governance does NOT import
// runtime/, adapters/, or cells/.
//
// Rule numbering: see kernel/governance/CLAUDE.md for the REF / TOPO /
// VERIFY / FMT / ADV / OUTGARD series and the ValidationResult schema.
package governance
